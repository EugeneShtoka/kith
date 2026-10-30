package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
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

// refusingBackend refuses every receipt.
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
		served <- daemon.Serve(ctx, ln, complete(t, &daemon.Daemon{
			Backend: b, Streams: daemon.NewStreams(), State: daemon.NewState(),
			Log: logging.New(logs, logging.Options{Level: slog.LevelDebug}),
		}))
	}()
	t.Cleanup(func() {
		cancel()
		if serr := <-served; serr != nil {
			t.Errorf("Serve() = %v", serr)
		}
	})
	return daemon.NewRemote(path), logs
}

// A failed call is logged with name and reason; the client gets the reason verbatim.
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
	for _, want := range []string{"level=WARN", "op=Rooms", "M_FORBIDDEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("daemon log %q lacks %q", out, want)
		}
	}
}

// A network that cannot do what was asked answers so; the journal does not keep it as a
// failure.
func TestANetworkThatCannotIsNoFailure(t *testing.T) {
	t.Parallel()
	r, logs := serveLogged(t, &refusingBackend{fakeBackend{err: fmt.Errorf("%w: groupings on signal", api.ErrNotOnNetwork)}})

	if _, err := r.Rooms(context.Background()); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Fatalf("Rooms() = %v, want ErrNotOnNetwork", err)
	}
	if out := logs.String(); strings.Contains(out, "level=WARN") {
		t.Errorf("logged as a failure: %q", out)
	}
}

// The reason behind a counted failure crosses the socket.
func TestMarkRoomsReadCarriesTheFirstError(t *testing.T) {
	t.Parallel()
	r, _ := serveLogged(t, &refusingBackend{})

	res, err := r.MarkRoomsRead(context.Background(), []domain.RoomID{mx + "!a:x", mx + "!b:x"}, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead() = %v", err)
	}
	if res.Failed != 2 || !strings.Contains(res.FirstError, "M_BAD_JSON") {
		t.Errorf("MarkRoomsRead() = %+v, want 2 failed with the reason", res)
	}
}

// A message gone from its network, or one its adapter holds no copy of, is an answer, not
// a fault: it crosses as ErrNoSuchMessage in the adapter's words, coded not_found (what
// the desktop reads), and the journal does not keep it as a failure.
func TestAMessageThatIsGoneCrossesAsNotFound(t *testing.T) {
	t.Parallel()
	room, event := domain.RoomID("telegram:1/-100"), domain.EventID("telegram:1/-100/7")
	gone := fmt.Errorf("telegram: fetch %s: telegram: the message is gone: %w", event, api.ErrNoSuchMessage)
	logs := &syncBuffer{}
	h := serve(t, &recording{failWith: gone}, func(d *daemon.Daemon) {
		d.Log = logging.New(logs, logging.Options{Level: slog.LevelDebug})
	})
	ctx := context.Background()

	_, err := h.client().FetchEvent(ctx, room, event)
	if !errors.Is(err, api.ErrNoSuchMessage) || !strings.Contains(err.Error(), "telegram: the message is gone") {
		t.Errorf("FetchEvent() = %v, want ErrNoSuchMessage in the adapter's words", err)
	}
	_, err = daemon.Client(h.path).FetchEvent(ctx, connect.NewRequest(&v1.FetchEventRequest{RoomId: string(room), EventId: string(event)}))
	if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Errorf("FetchEvent() on the wire = %v (%v), want not_found", err, code)
	}
	if out := logs.String(); strings.Contains(out, "level=WARN") {
		t.Errorf("logged as a failure: %q", out)
	}
}
