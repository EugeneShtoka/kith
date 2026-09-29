package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A client that subscribed and then stopped reading (a suspended TUI) must not hold
// shutdown past its timeout, nor leave its handler running once Serve has returned.
func TestShutdownFreesAStreamWhoseClientStoppedReading(t *testing.T) {
	t.Parallel()

	// Short: a unix socket path is limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "mx") //nolint:usetesting // a unix socket path is limited to about 100 bytes; t.TempDir is longer
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{Nop: nopWithChannels()}
	streams := daemon.NewStreams()
	pumpCtx, stopPump := context.WithCancel(context.Background())
	pumped := make(chan struct{})
	go func() { defer close(pumped); streams.Run(pumpCtx, backend) }()
	t.Cleanup(func() { stopPump(); <-pumped })

	const timeout = 10 * time.Second // the daemon's shutdownTimeout
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: backend, Streams: streams, State: daemon.NewState(),
		})
	}()

	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// A Connect server-stream call with an empty request, and then never a read.
	if _, err := fmt.Fprint(conn, "POST /backend.v1.BackendService/Messages HTTP/1.1\r\n"+
		"Host: mx\r\nContent-Type: application/connect+proto\r\nConnect-Protocol-Version: 1\r\n"+
		"Content-Length: 5\r\n\r\n\x00\x00\x00\x00\x00"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return streams.MessageSubscribers() == 1 }, "the stream to subscribe")

	// Far more than the socket's buffers and the hub's queue hold. Paced, so the
	// handler keeps up while it can write: it ends stuck in a write that only closing
	// the connection ends.
	big := strings.Repeat("x", 64<<10)
	for range 300 { // about 19 MiB
		backend.Msgs <- domain.Message{ID: "$m", RoomID: "!a:x", Body: big}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil: a client that stopped reading is not a daemon failure", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return")
	}
	if took := time.Since(start); took > timeout+3*time.Second {
		t.Errorf("shutdown took %s, want about %s", took, timeout)
	}
	if n := streams.MessageSubscribers(); n != 0 {
		t.Errorf("%d stream handler(s) still running after Serve returned", n)
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// failingListener hands out connections until broken, then fails every Accept, as a
// listener whose socket was removed or whose descriptor limit was hit does.
type failingListener struct {
	net.Listener
	broken chan struct{}
}

func (l *failingListener) Accept() (net.Conn, error) {
	type accepted struct {
		conn net.Conn
		err  error
	}
	got := make(chan accepted, 1)
	go func() {
		conn, err := l.Listener.Accept()
		got <- accepted{conn, err}
	}()
	select {
	case a := <-got:
		return a.conn, a.err
	case <-l.broken:
		return nil, errors.New("accept: the listener broke")
	}
}

// When the listener fails, Serve still cuts off and waits for the handlers it
// started before it returns: the caller closes the stores next.
func TestAFailedListenerStillDrainsTheHandlers(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("", "mx") //nolint:usetesting // a unix socket path is limited to about 100 bytes; t.TempDir is longer
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	inner, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	ln := &failingListener{Listener: inner, broken: make(chan struct{})}
	t.Cleanup(func() { _ = inner.Close() })

	backend := &fakeBackend{Nop: nopWithChannels()}
	streams := daemon.NewStreams()
	pumpCtx, stopPump := context.WithCancel(context.Background())
	pumped := make(chan struct{})
	go func() { defer close(pumped); streams.Run(pumpCtx, backend) }()
	t.Cleanup(func() { stopPump(); <-pumped })

	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(context.Background(), ln, &daemon.Daemon{
			Backend: backend, Streams: streams, State: daemon.NewState(),
		})
	}()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprint(conn, "POST /backend.v1.BackendService/Messages HTTP/1.1\r\n"+
		"Host: mx\r\nContent-Type: application/connect+proto\r\nConnect-Protocol-Version: 1\r\n"+
		"Content-Length: 5\r\n\r\n\x00\x00\x00\x00\x00"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return streams.MessageSubscribers() == 1 }, "the stream to subscribe")

	close(ln.broken)
	select {
	case err := <-served:
		if err == nil || errors.Is(err, daemon.ErrHandlersRunning) {
			t.Fatalf("Serve() = %v, want the listener's failure, with the handlers drained", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return")
	}
	if n := streams.MessageSubscribers(); n != 0 {
		t.Errorf("%d stream handler(s) still running after Serve returned", n)
	}
}

// lifetimeBackend's MarkRoomsRead runs until its context ends, as a long space
// sweep on the daemon's lifetime does.
type lifetimeBackend struct {
	fakeBackend
	started chan struct{}
}

func (b *lifetimeBackend) MarkRoomsRead(ctx context.Context, _ []domain.RoomID, _ bool) (domain.ReadResult, error) {
	close(b.started)
	<-ctx.Done()
	return domain.ReadResult{}, ctx.Err()
}

// A handler on the daemon's lifetime outlives its connection, so closing connections
// does not end it. A broken listener must, or the drain times out and the caller
// skips Stop with the stores left open.
func TestAFailedListenerEndsLifetimeHandlers(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("", "mx") //nolint:usetesting // a unix socket path is limited to about 100 bytes; t.TempDir is longer
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	inner, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	ln := &failingListener{Listener: inner, broken: make(chan struct{})}
	t.Cleanup(func() { _ = inner.Close() })

	backend := &lifetimeBackend{fakeBackend: fakeBackend{Nop: nopWithChannels()}, started: make(chan struct{})}
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(context.Background(), ln, &daemon.Daemon{
			Backend: backend, Streams: daemon.NewStreams(), State: daemon.NewState(),
		})
	}()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprint(conn, "POST /backend.v1.BackendService/MarkRoomsRead HTTP/1.1\r\n"+
		"Host: mx\r\nContent-Type: application/proto\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(15 * time.Second):
		t.Fatal("MarkRoomsRead never started")
	}

	close(ln.broken)
	select {
	case err := <-served:
		if err == nil || errors.Is(err, daemon.ErrHandlersRunning) {
			t.Fatalf("Serve() = %v, want the listener's failure, with the handler ended", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return")
	}
}
