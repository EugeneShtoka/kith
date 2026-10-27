package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// lockedInstance is an instance whose cache was written under key, with its socket
// lock held, as kithd holds it before opening the cache.
func lockedInstance(t *testing.T) (domain.Storage, *daemon.Lock, []byte) {
	t.Helper()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	key := bytes.Repeat([]byte{5}, sealeddb.KeyLen)
	cache, err := db.Open(context.Background(), storage.CachePath(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.SaveMessages(context.Background(), "!kept:example.org", []domain.Message{{
		ID: "$kept", RoomID: "!kept:example.org", Sender: "@a:example.org", Body: "kept", Timestamp: time.UnixMilli(1),
	}}); err != nil {
		t.Fatal(err)
	}
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
	lock, held, err := daemon.Acquire(storage)
	if err != nil || !held {
		t.Fatalf("Acquire() = %v, %v", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	return storage, lock, key
}

// A cache from before caches were encrypted is encrypted at the first start, which is
// said before (with its size) and after (with how long it took), so a slow first start
// reads as what it is; later starts say nothing of it.
func TestEncryptingAPlainCacheIsSaid(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	plain, err := driver.Open("file:" + storage.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plain.ExecContext(context.Background(), "CREATE TABLE written_before (v)"); err != nil {
		t.Fatal(err)
	}
	_ = plain.Close()
	for _, first := range []bool{true, false} {
		var logged bytes.Buffer
		cache, err := openCache(context.Background(), slog.New(slog.NewTextHandler(&logged, nil)), nil, storage)
		if err != nil {
			t.Fatal(err)
		}
		_ = cache.Close()
		for _, line := range []string{"encrypting the cache, one time", "the cache is encrypted"} {
			if said := strings.Contains(logged.String(), line); said != first {
				t.Errorf("first start %t: %q said %t; log:\n%s", first, line, said, logged.String())
			}
		}
	}
}

// keepKey puts key in the in-memory keyring where kithd reads the cache's.
func keepKey(t *testing.T, storage domain.Storage, key []byte) {
	t.Helper()
	if err := keyring.Set(storage.KeyringService, storage.Instance+"|cache-store", base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
}

// hold is kithd's log, holding the goroutine that opens the cache at its first
// "waiting for the cache" (logged after it read the keyring) until release. go-keyring's
// in-memory keyring is not safe to change while another goroutine reads it, so the
// test changes it only while that goroutine is held there.
type hold struct {
	once           sync.Once
	reached, freed chan struct{}
}

func newHold() *hold { return &hold{reached: make(chan struct{}), freed: make(chan struct{})} }

func (h *hold) Enabled(context.Context, slog.Level) bool { return true }
func (h *hold) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *hold) WithGroup(string) slog.Handler            { return h }

func (h *hold) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "waiting for the cache" {
		h.once.Do(func() {
			close(h.reached)
			<-h.freed
		})
	}
	return nil
}

// waited fails the test unless kithd came to wait for the cache.
func (h *hold) waited(t *testing.T) {
	t.Helper()
	select {
	case <-h.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("kithd did not wait for the cache")
	}
}

// release lets kithd go on waiting, now with the keyring as the test left it.
func (h *hold) release() { close(h.freed) }

// waitingSays waits for the daemon at storage to answer as not ready, saying want.
func waitingSays(t *testing.T, storage domain.Storage, want string) {
	t.Helper()
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready, _, lastErr, err := daemon.NewRemote(socket).Status(context.Background())
		if err == nil && !ready && strings.Contains(lastErr, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Status() = ready %t, %q, %v; want not ready saying %q", ready, lastErr, err, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// kithd started while the keyring is locked (a boot with no prompter) waits, telling
// clients why, and once the keyring opens it opens the cache as it was and only then
// the networks, whose stores' keys the keyring now gives: none is left unable to
// connect for the keyring having been locked at start.
func TestALockedKeyringIsWaitedForAndThenEverythingOpens(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	storage, lock, key := lockedInstance(t)
	keyring.MockInitWithError(errors.New("the keyring is locked"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type opened struct {
		cache *db.Cache
		err   error
	}
	done := make(chan opened, 1)
	held := newHold()
	go func() {
		c, err := openCache(ctx, slog.New(held), lock, storage)
		done <- opened{c, err}
	}()
	held.waited(t)
	waitingSays(t, storage, "unlock the keyring")

	keyring.MockInit()
	keepKey(t, storage, key)
	held.release()
	got := <-done
	if got.err != nil || got.cache == nil {
		t.Fatalf("openCache() after the keyring opened = %v, %v", got.cache, got.err)
	}
	defer func() { _ = got.cache.Close() }()
	if msgs, _ := got.cache.Messages(ctx, "!kept:example.org", -1); len(msgs) != 1 || msgs[0].Body != "kept" {
		t.Errorf("the cache opened after the wait holds %v; want the kept message", msgs)
	}
	backend := newServed(ctx, got.cache, slog.New(slog.DiscardHandler), config.Config{}, storage)
	defer func() {
		backend.Stop()
		backend.Close(slog.New(slog.DiscardHandler))
	}()
	var names []string
	for _, s := range backend.stores {
		names = append(names, s.name)
	}
	for _, want := range []string{"WhatsApp", "Messenger"} {
		if !slices.Contains(names, want) {
			t.Errorf("after the wait the stores open are %v; %s is not, so it cannot connect", names, want)
		}
	}
}

// A keyring entry replaced (another key where the cache's was) is waited out the same
// way, saying the key does not open the cache: the cache is not rebuilt or replaced,
// and putting the entry back opens it with everything in it.
func TestAReplacedKeyIsWaitedOutNotAnsweredWithANewCache(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	storage, lock, key := lockedInstance(t)
	keyring.MockInit()
	keepKey(t, storage, bytes.Repeat([]byte{6}, sealeddb.KeyLen))
	before, err := os.ReadFile(storage.CachePath())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan *db.Cache, 1)
	held := newHold()
	go func() {
		c, err := openCache(ctx, slog.New(held), lock, storage)
		if err != nil {
			t.Errorf("openCache() error = %v", err)
		}
		done <- c
	}()
	held.waited(t)
	waitingSays(t, storage, "does not open the cache")
	if after, _ := os.ReadFile(storage.CachePath()); !bytes.Equal(after, before) {
		t.Error("the cache changed while its key was wrong")
	}

	keepKey(t, storage, key)
	held.release()
	cache := <-done
	if cache == nil {
		t.Fatal("no cache after the key came back")
	}
	defer func() { _ = cache.Close() }()
	if msgs, _ := cache.Messages(ctx, "!kept:example.org", -1); len(msgs) != 1 {
		t.Errorf("the cache holds %v after its key came back; want the kept message", msgs)
	}
}
