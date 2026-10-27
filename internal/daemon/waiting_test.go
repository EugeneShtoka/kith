package daemon_test

// A daemon that cannot start yet (its cache will not open) and says why.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const lockedWhy = "the cache is locked: the keyring is not answering; unlock it"

// waitingDaemon serves on a new socket as kithd does while its cache is locked.
func waitingDaemon(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "kithd.sock")
	waitingAt(t, socket, func() string { return lockedWhy })
	return socket
}

// waitingAt serves on socket as kithd does while its cache is locked, saying why(),
// until the returned stop.
func waitingAt(t *testing.T, socket string, why func() string) (stop func()) {
	t.Helper()
	ln, err := daemon.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.ServeWaiting(ctx, ln, why) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-served; err != nil {
				t.Errorf("ServeWaiting: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// soon is how long a client may take to say why: one probe, not a readiness timeout.
const soon = 2 * time.Second

// isWaiting fails unless err says the daemon is waiting, and why.
func isWaiting(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, api.ErrDaemonWaiting) {
		t.Fatalf("%s: err = %v, want api.ErrDaemonWaiting", what, err)
	}
	if !strings.Contains(err.Error(), lockedWhy) {
		t.Errorf("%s: %q does not say why (%q)", what, err, lockedWhy)
	}
}

// Attaching says why at once, rather than after its readiness timeout.
func TestAttachingToAWaitingDaemonSaysWhyAtOnce(t *testing.T) {
	t.Parallel()
	socket := waitingDaemon(t)

	start := time.Now()
	_, err := daemon.Attach(context.Background(), socket, time.Minute)
	if took := time.Since(start); took > soon {
		t.Errorf("Attach took %s, want the reason within %s", took, soon)
	}
	isWaiting(t, "Attach", err)
}

// A call refused while the daemon waits carries the reason and the sentinel, so a
// client that never attached (kith-mcp) answers with it.
func TestACallToAWaitingDaemonSaysWhy(t *testing.T) {
	t.Parallel()
	r := daemon.NewRemote(waitingDaemon(t))

	_, err := r.Rooms(context.Background())
	isWaiting(t, "Rooms", err)
}

// A stream refused while the daemon waits says why, and was never open: kith-notify
// backs off instead of counting it as listening.
func TestAStreamToAWaitingDaemonSaysWhyAndNeverOpened(t *testing.T) {
	t.Parallel()
	r := daemon.NewRemote(waitingDaemon(t))

	opened := false
	err := r.Notifications(context.Background(), func() { opened = true }, func(daemon.Notice) {})
	isWaiting(t, "Notifications", err)
	if opened {
		t.Error("the refused stream was reported open")
	}
}

// WaitOut says why at once and each new reason, rides over the gap between kithd's
// waiting socket and its real one, and returns once kithd answers without waiting.
func TestWaitOutSaysWhyAndReturnsOnceKithdStarts(t *testing.T) {
	t.Parallel()
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "t"}
	socket := storage.SocketPath()
	var why atomic.Pointer[string]
	first := lockedWhy
	why.Store(&first)
	stopWaiting := waitingAt(t, socket, func() string { return *why.Load() })

	var mu sync.Mutex
	var said []string
	start := time.Now()
	saidAt := make(chan time.Duration, 1)
	returned := make(chan error, 1)
	go func() {
		returned <- daemon.WaitOut(context.Background(), storage, func(w string) {
			mu.Lock()
			defer mu.Unlock()
			if len(said) == 0 {
				saidAt <- time.Since(start)
			}
			said = append(said, w)
		})
	}()
	if took := <-saidAt; took > soon {
		t.Errorf("said why after %s, want within %s", took, soon)
	}
	second := "the cache is locked: its key does not open it; put the right one back"
	why.Store(&second)
	waitSaid := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			got := len(said)
			mu.Unlock()
			if got >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("said %d reasons, want %d", got, n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitSaid(2)

	// The waiting socket closes; for a while nothing answers, as while kithd opens its
	// cache and starts its networks. That is not the end of the wait.
	stopWaiting()
	gap := 3 * time.Second
	select {
	case err := <-returned:
		t.Fatalf("WaitOut returned %v while kithd was starting", err)
	case <-time.After(gap):
	}
	started := time.Now()
	runDaemon(t, socket, apitest.Nop{})
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("WaitOut = %v once kithd started", err)
		}
		if took := time.Since(started); took > soon {
			t.Errorf("WaitOut returned %s after kithd started", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitOut did not return once kithd started")
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{lockedWhy, second}; strings.Join(said, "|") != strings.Join(want, "|") {
		t.Errorf("said %q, want %q", said, want)
	}
}

// A one-shot command reaching a waiting kithd fails at once with why, and does not
// start a second one.
func TestEnsuringAWaitingKithdFailsAtOnceWithWhy(t *testing.T) {
	t.Parallel()
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "t"}
	waitingAt(t, storage.SocketPath(), func() string { return lockedWhy })

	start := time.Now()
	_, note, err := daemon.Ensure(context.Background(), storage, daemon.Launch{}, time.Minute)
	if took := time.Since(start); took > soon {
		t.Errorf("Ensure took %s, want the reason within %s", took, soon)
	}
	isWaiting(t, "Ensure", err)
	if note != "" {
		t.Errorf("Ensure noted %q: it claims to have started a kithd", note)
	}
}
