package db

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// sealedRoom is the room the encryption tests fill.
const sealedRoom = domain.RoomID("!sealed:example.org")

// plainFilesIn names every file in dir that holds a plain SQLite database or log: the
// history in the clear.
func plainFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var plain []string
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		if err != nil || e.IsDir() {
			continue
		}
		head := make([]byte, 16)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		// A database starts "SQLite format 3", a write-ahead log with its magic.
		if bytes.HasPrefix(head[:n], []byte("SQLite format 3")) || bytes.HasPrefix(head[:n], []byte{0x37, 0x7f, 0x06, 0x82}) ||
			bytes.HasPrefix(head[:n], []byte{0x37, 0x7f, 0x06, 0x83}) {
			plain = append(plain, e.Name())
		}
	}
	return plain
}

// A key that does not open the cache (the keyring's entry replaced) is said, and the
// cache is left exactly as it was: never rebuilt, set aside or written over.
func TestAWrongKeyLeavesTheCacheUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	cache, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, cache, sealedRoom, domain.Message{ID: "$kept", RoomID: sealedRoom, Sender: "@a:example.org", Body: "kept", Timestamp: time.UnixMilli(1)})
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		body, _ := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		before[e.Name()] = body
	}
	replaced := bytes.Repeat([]byte{9}, len(testKey))
	if _, err = Open(ctx, path, replaced); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("Open(another key) error = %v; want ErrWrongKey", err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Errorf("files after a wrong key: %d, before: %d", len(after), len(before))
	}
	for _, e := range after {
		body, _ := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		if !bytes.Equal(body, before[e.Name()]) {
			t.Errorf("%s changed under a wrong key", e.Name())
		}
	}
	reopened, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got, _ := reopened.Messages(ctx, sealedRoom, -1); len(got) != 1 || got[0].Body != "kept" {
		t.Errorf("after a wrong key the cache holds %v", got)
	}
}

// The copy a rebuild sets aside is written encrypted, not left in the clear for the
// next open to encrypt: a crash between the two would leave the history readable.
func TestARebuildSetsTheOldCacheAsideEncrypted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	cache := fillCache(t, path, 20)
	if asideErr, err := rebuild(context.Background(), cache.db, path, testKey); err != nil || asideErr != nil {
		t.Fatalf("rebuild() = %v, %v", asideErr, err)
	}
	if left := plainFilesIn(t, dir); len(left) > 0 {
		t.Errorf("the rebuild set the old cache aside in the clear: %v", left)
	}
	ownersOnly(t, dir)
}

// ownersOnly checks every file in dir is the owner's alone, whatever the umask.
func ownersOnly(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s is %v; want 0600, the owner's alone", e.Name(), perm)
		}
	}
}

// A snapshot (what a backup carries) is encrypted under the backup's key, not the
// cache's, and is never written in the clear.
func TestCopiesOfTheCacheAreNeverInTheClear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	fillCache(t, path, 30)
	backupKey := bytes.Repeat([]byte{3}, len(testKey))
	snap := filepath.Join(dir, "snapshot.db")
	if err := Snapshot(ctx, path, testKey, snap, backupKey); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(ctx, snap, testKey, true); !errors.Is(err, ErrWrongKey) {
		t.Errorf("the cache's own key opened the snapshot: %v", err)
	}
	if left := plainFilesIn(t, dir); len(left) > 0 {
		t.Errorf("in the clear: %v", left)
	}

}
