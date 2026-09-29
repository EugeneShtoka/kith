package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// syncBuffer is a bytes.Buffer the server's goroutines may write while a test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// refusingBackend fails the way the motivating incident did: every receipt refused.
type refusingBackend struct {
	fakeBackend
}

func (refusingBackend) MarkRoomsRead(_ context.Context, roomIDs []domain.RoomID, _ bool) (domain.ReadResult, error) {
	return domain.ReadResult{Failed: len(roomIDs), FirstError: "M_BAD_JSON (HTTP 400): Invalid JSON"}, nil
}

// serveLogged runs a daemon whose failure log goes to the returned buffer.
func serveLogged(t *testing.T, b *refusingBackend) (*daemon.Remote, *syncBuffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: b, Streams: daemon.NewStreams(), State: daemon.NewState(),
			Log: logging.New(logs, logging.Options{Level: slog.LevelDebug}),
		})
	}()
	t.Cleanup(func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("Serve() = %v", serr)
		}
	})
	return daemon.NewRemote(path), logs
}

// A failed call reaches the journal with its name and reason, and the client gets the
// reason word for word (no "internal:" noise).
func TestFailedCallIsLoggedWithItsReason(t *testing.T) {
	t.Parallel()
	r, logs := serveLogged(t, &refusingBackend{fakeBackend{err: errors.New("matrix: rooms: M_FORBIDDEN (HTTP 403): nope")}})

	_, err := r.Rooms(context.Background())
	if err == nil || !strings.Contains(err.Error(), "M_FORBIDDEN (HTTP 403): nope") {
		t.Fatalf("Rooms() = %v, want the daemon's reason", err)
	}
	if strings.Contains(err.Error(), "internal:") {
		t.Errorf("Rooms() = %q, want no Connect code prefix", err)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", `msg="call failed"`, "op=Rooms", "M_FORBIDDEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("daemon log %q lacks %q", out, want)
		}
	}
}

// The reason behind a counted failure crosses the socket.
func TestMarkRoomsReadCarriesTheFirstError(t *testing.T) {
	t.Parallel()
	r, _ := serveLogged(t, &refusingBackend{})

	res, err := r.MarkRoomsRead(context.Background(), []domain.RoomID{"!a:x", "!b:x"}, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead() = %v", err)
	}
	if res.Failed != 2 || !strings.Contains(res.FirstError, "M_BAD_JSON") {
		t.Errorf("MarkRoomsRead() = %+v, want 2 failed with the reason", res)
	}
}
