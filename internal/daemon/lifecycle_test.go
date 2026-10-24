package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Readiness means a sync response arrived; failure and recovery are tracked.
func TestStateReadiness(t *testing.T) {
	t.Parallel()

	s := daemon.NewState()
	if ready, at, lastErr := s.Snapshot(); ready || !at.IsZero() || lastErr != "" {
		t.Errorf("fresh Snapshot() = %t, %v, %q; want not ready, zero, empty", ready, at, lastErr)
	}

	synced := time.Now()
	s.Synced(synced)
	ready, at, lastErr := s.Snapshot()
	if !ready || !at.Equal(synced) || lastErr != "" {
		t.Errorf("after Synced: %t, %v, %q; want ready, %v, empty", ready, at, lastErr, synced)
	}

	// A dead sync loop must not look healthy.
	s.Failed(errors.New("sync: connection refused"))
	ready, _, lastErr = s.Snapshot()
	if ready {
		t.Error("ready = true after the sync loop failed, want false")
	}
	if !strings.Contains(lastErr, "connection refused") {
		t.Errorf("lastError = %q, want the sync failure", lastErr)
	}

	// A later success clears the failure.
	s.Synced(time.Now())
	if ready, _, lastErr = s.Snapshot(); !ready || lastErr != "" {
		t.Errorf("after recovery: ready = %t, lastError = %q; want true, empty", ready, lastErr)
	}

	// A clean shutdown returns nil, which is not a failure.
	s.Failed(nil)
	if ready, _, lastErr = s.Snapshot(); !ready || lastErr != "" {
		t.Errorf("Failed(nil) changed state to %t, %q; want it left alone", ready, lastErr)
	}
}

// The first daemon holds the lock; the second is told so.
func TestLockAdmitsOneDaemon(t *testing.T) {
	t.Parallel()

	user := lockUser(t)

	first, held, err := daemon.Acquire(user)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if !held {
		t.Fatal("first Acquire() held = false, want true")
	}

	// Not an error: two clients racing to auto-spawn is expected.
	second, held, err := daemon.Acquire(user)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if held {
		t.Error("second Acquire() held = true, want false — two daemons would share the crypto store")
	}
	if second != nil {
		t.Error("second Acquire() returned a lock, want nil")
	}

	if rerr := first.Release(); rerr != nil {
		t.Fatalf("Release() error = %v", rerr)
	}
	// Released means a restarted daemon can take over.
	third, held, err := daemon.Acquire(user)
	if err != nil || !held {
		t.Fatalf("Acquire() after Release() = %t, %v; want held", held, err)
	}
	t.Cleanup(func() { _ = third.Release() })
}

// Only the lock holder removes a stale socket.
func TestLockRemovesStaleSocket(t *testing.T) {
	t.Parallel()

	user := lockUser(t)
	lock, held, err := daemon.Acquire(user)
	if err != nil || !held {
		t.Fatalf("Acquire() = %t, %v; want held", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	// What a killed daemon leaves behind.
	if werr := os.WriteFile(lock.Socket(), nil, 0o600); werr != nil {
		t.Fatalf("plant a stale socket: %v", werr)
	}
	ln, err := lock.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen() over a stale socket = %v, want it removed and replaced", err)
	}
	if cerr := ln.Close(); cerr != nil {
		t.Errorf("Close() = %v", cerr)
	}

	// Without the lock, Listen refuses a socket in the way.
	if werr := os.WriteFile(lock.Socket(), nil, 0o600); werr != nil {
		t.Fatalf("re-plant a stale socket: %v", werr)
	}
	if _, err := daemon.Listen(context.Background(), lock.Socket()); err == nil {
		t.Error("Listen() over an existing socket = nil, want a refusal")
	}
}

// Release leaves no socket behind.
func TestLockReleaseRemovesSocket(t *testing.T) {
	t.Parallel()

	user := lockUser(t)
	lock, held, err := daemon.Acquire(user)
	if err != nil || !held {
		t.Fatalf("Acquire() = %t, %v; want held", held, err)
	}
	ln, err := lock.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	_ = ln.Close()
	socket := lock.Socket()
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after Release(): %v", err)
	}
}

// The lock sits beside the socket and shares its stem.
func TestLockAndSocketPathsPair(t *testing.T) {
	t.Parallel()

	storage := domain.Storage{Instance: "a1b2c3", RuntimeDir: "/run/user/1000/kith"}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	lock := storage.LockPath()
	if filepath.Dir(socket) != filepath.Dir(lock) {
		t.Errorf("lock %q is not beside socket %q", lock, socket)
	}
	if strings.TrimSuffix(filepath.Base(socket), ".sock") != strings.TrimSuffix(filepath.Base(lock), ".lock") {
		t.Errorf("lock %q and socket %q do not share a stem", lock, socket)
	}
}

// Status lets the client tell "starting" from "broken".
func TestStatusRoundTrip(t *testing.T) {
	t.Parallel()

	h := serve(t, &fakeBackend{Nop: nopWithChannels()})
	r := h.client()
	ctx := context.Background()

	ready, at, lastErr, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if ready || !at.IsZero() || lastErr != "" {
		t.Errorf("Status() = %t, %v, %q; want a daemon that has not synced yet", ready, at, lastErr)
	}

	synced := time.UnixMilli(time.Now().UnixMilli())
	h.state.Synced(synced)
	ready, at, lastErr, err = r.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !ready || !at.Equal(synced) || lastErr != "" {
		t.Errorf("Status() = %t, %v, %q; want ready at %v", ready, at, lastErr, synced)
	}

	h.state.Failed(errors.New("sync: connection refused"))
	ready, _, lastErr, err = r.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if ready || !strings.Contains(lastErr, "connection refused") {
		t.Errorf("Status() = %t, %q; want not ready with the daemon's own error", ready, lastErr)
	}
}

// Attach waits for readiness, not a listening socket: a cold daemon's empty cache
// looks like an empty account.
func TestAttachWaitsForReady(t *testing.T) {
	t.Parallel()

	h := serve(t, &fakeBackend{Nop: nopWithChannels()})
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.state.Synced(time.Now())
	}()

	r, err := daemon.Attach(context.Background(), h.path, settle)
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if r == nil {
		t.Fatal("Attach() returned no client")
	}
}

// A daemon that never becomes ready fails with its own reason.
func TestAttachTimeoutReportsTheDaemonsError(t *testing.T) {
	t.Parallel()

	h := serve(t, &fakeBackend{Nop: nopWithChannels()})
	h.state.Failed(errors.New("sync: invalid access token"))

	_, err := daemon.Attach(context.Background(), h.path, 200*time.Millisecond)
	if err == nil {
		t.Fatal("Attach() error = nil, want a timeout")
	}
	if !strings.Contains(err.Error(), "invalid access token") {
		t.Errorf("Attach() error = %q, want it to carry the daemon's own reason", err)
	}
}

// With no daemon, Attach fails rather than blocking or spawning (that is Ensure's job).
func TestAttachWithNoDaemon(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := daemon.Attach(context.Background(), socket, 200*time.Millisecond); err == nil {
		t.Error("Attach() error = nil, want a failure with no daemon listening")
	}
}

// Ensure does not spawn when a daemon is serving, and gives no note.
func TestEnsureAttachesToARunningDaemon(t *testing.T) {
	t.Parallel()

	user := lockUser(t)
	lock, held, err := daemon.Acquire(user)
	if err != nil || !held {
		t.Fatalf("Acquire() = %t, %v; want held", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	ln, err := lock.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	backend := &fakeBackend{Nop: nopWithChannels()}
	streams := daemon.NewStreams()
	state := daemon.NewState()
	state.Synced(time.Now()) // ready before anyone attaches, as an autostarted daemon is
	served := make(chan error, 1)
	pumped := make(chan struct{})
	go func() { defer close(pumped); streams.Run(ctx, backend) }()
	go func() {
		served <- daemon.Serve(ctx, ln, complete(t, &daemon.Daemon{Backend: backend, Streams: streams, State: state}))
	}()
	t.Cleanup(func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("Serve() = %v, want nil", serr)
		}
		<-pumped
	})

	// The default config, no profile.
	r, note, err := daemon.Ensure(ctx, user, daemon.Launch{}, settle)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if r == nil {
		t.Fatal("Ensure() returned no client")
	}
	if note != "" {
		t.Errorf("Ensure() note = %q, want empty — nothing was started, so nothing was missed", note)
	}
}

// Maintenance operations cross the wire: the client never opens the databases.
func TestMaintenanceRoundTrip(t *testing.T) {
	t.Parallel()

	b := &maintenance{keys: 42}
	h := serve(t, b)
	r := h.client()
	ctx := context.Background()

	if err := r.ClearCache(ctx); err != nil {
		t.Fatalf("ClearCache() error = %v", err)
	}
	if !b.cleared {
		t.Error("ClearCache() did not reach the daemon's backend")
	}

	keys, err := r.RestoreKeyBackup(ctx, domain.RoomOwner(mx), "EsTx recovery key")
	if err != nil {
		t.Fatalf("RestoreKeyBackup() error = %v", err)
	}
	if keys != 42 {
		t.Errorf("RestoreKeyBackup() = %d, want 42", keys)
	}
	if b.gotSecret != "EsTx recovery key" {
		t.Errorf("backend saw secret %q, want it to arrive in the request body", b.gotSecret)
	}
}

// maintenance records the two store operations.
type maintenance struct {
	fakeBackend
	keys      int
	cleared   bool
	gotSecret string
}

func (m *maintenance) ClearCache(context.Context) error {
	m.cleared = true
	return nil
}

func (m *maintenance) RestoreKeyBackup(_ context.Context, _ domain.RoomOwner, secret string) (int, error) {
	m.gotSecret = secret
	return m.keys, nil
}

// nopWithChannels gives Streams.Run real channels instead of five nil ones.
func nopWithChannels() apitest.Nop {
	return apitest.Nop{
		Msgs:    make(chan domain.Message),
		Unreads: make(chan domain.Unread),
		Reacts:  make(chan domain.ReactionUpdate),
		Invs:    make(chan []domain.Room),
		Verifs:  make(chan domain.Verification),
	}
}

// lockUser returns an instance name unique to the test, so parallel tests take
// different locks.
func lockUser(t *testing.T) domain.Storage {
	t.Helper()
	// Its own runtime directory: Release leaves the lock file behind.
	return domain.Storage{Instance: strings.ReplaceAll(t.Name(), "/", "-"), RuntimeDir: filepath.Join(t.TempDir(), "kith")}
}
