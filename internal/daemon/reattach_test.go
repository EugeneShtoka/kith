package daemon_test

// A daemon restarting under an attached client.

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The client reattaches by itself, and the UI's channel never closes.
func TestClientReattachesWhenTheDaemonRestarts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	feed := make(chan domain.Message, 4)
	backend := apitest.Nop{Msgs: feed}

	first := runDaemon(t, path, backend)
	client := daemon.NewRemote(path)
	attached := make(chan error, 1)
	go func() { attached <- client.Start(context.Background()) }()
	t.Cleanup(client.Stop)

	first.waitAttached(t, 1)
	feed <- domain.Message{ID: mx + "$before", RoomID: mx + "!r:x", Body: "before the restart"}
	if got := nextMessage(t, client.Messages()); got.ID != mx+"$before" {
		t.Fatalf("first message = %+v, want $before", got)
	}

	// The daemon and its socket file go away, as a systemd restart leaves them.
	first.stop(t)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove socket: %v", err)
	}
	if got := waitAttachment(t, client.Attached()); got {
		t.Error("Attached() reported true when the daemon went away, want false")
	}

	second := runDaemon(t, path, backend)
	if got := waitAttachment(t, client.Attached()); !got {
		t.Error("Attached() reported false after the daemon came back, want true")
	}
	second.waitAttached(t, 1)

	// A message sent after the restart arrives on the channel the UI held all along.
	feed <- domain.Message{ID: mx + "$after", RoomID: mx + "!r:x", Body: "after the restart"}
	if got := nextMessage(t, client.Messages()); got.ID != mx+"$after" {
		t.Fatalf("message after the restart = %+v, want $after", got)
	}

	// Stop still ends it; a retry loop outliving the client would leak.
	client.Stop()
	select {
	case err := <-attached:
		if err != nil {
			t.Errorf("Start() = %v, want nil after Stop", err)
		}
	case <-time.After(settle):
		t.Fatal("Stop did not end Start")
	}
	if _, open := <-client.Messages(); open {
		t.Error("the message channel is still open after Stop, want closed")
	}
}

// daemonRun is one lifetime of a daemon on a fixed path.
type daemonRun struct {
	streams *daemon.Streams
	served  chan error
	pumped  chan struct{}
	cancel  context.CancelFunc
	stopped bool
}

// runDaemon serves b on path until stopped or the test ends.
func runDaemon(t *testing.T, path string, b api.Backend, with ...func(*daemon.Daemon)) *daemonRun {
	t.Helper()

	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	streams := daemon.NewStreams()
	notifications, err := daemon.NewNotifications(asStarted(
		config.Config{}), scopeOnly{b})
	if err != nil {
		cancel()
		t.Fatalf("NewNotifications: %v", err)
	}
	run := &daemonRun{
		streams: streams, cancel: cancel,
		served: make(chan error, 1), pumped: make(chan struct{}),
	}
	go func() { defer close(run.pumped); streams.Run(ctx, b) }()
	d := &daemon.Daemon{
		Backend: b, Streams: streams, State: daemon.NewState(),
		Notifications: notifications, Reload: (&reloadStub{}).fn,
	}
	for _, f := range with {
		f(d)
	}
	complete(t, d)
	go func() { run.served <- daemon.Serve(ctx, ln, d) }()
	t.Cleanup(func() { run.stop(t) })
	return run
}

// stop ends this daemon and waits until the path is free. Idempotent.
func (d *daemonRun) stop(t *testing.T) {
	t.Helper()

	if d.stopped {
		return
	}
	d.stopped = true
	d.cancel()
	if err := <-d.served; err != nil {
		t.Errorf("Serve() = %v, want nil", err)
	}
	<-d.pumped
}

// waitAttached blocks until want clients have subscribed to this daemon's streams.
func (d *daemonRun) waitAttached(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if d.streams.Attached() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d client(s); %d attached", want, d.streams.Attached())
}

// nextMessage reads one streamed message, failing rather than hanging.
func nextMessage(t *testing.T, ch <-chan domain.Message) domain.Message {
	t.Helper()

	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("the message channel closed; the client detached for good")
		}
		return msg
	case <-time.After(settle):
		t.Fatal("no message arrived")
		return domain.Message{}
	}
}

// waitAttachment reads one attachment change; the reconnect runs on a real clock.
func waitAttachment(t *testing.T, ch <-chan bool) bool {
	t.Helper()

	select {
	case attached, ok := <-ch:
		if !ok {
			t.Fatal("the attachment channel closed")
		}
		return attached
	case <-time.After(settle):
		t.Fatal("no attachment change arrived")
		return false
	}
}

// A daemon gone between the readiness probe and the first subscribe does not leave
// the client stream-less.
func TestAFirstAttachThatRacesARestartStillReattaches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	feed := make(chan domain.Message, 4)
	backend := apitest.Nop{Msgs: feed}

	// Already gone by the time the client subscribes.
	gone := runDaemon(t, path, backend)
	gone.stop(t)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove socket: %v", err)
	}

	client := daemon.NewRemote(path)
	ended := make(chan error, 1)
	go func() { ended <- client.Start(context.Background()) }()
	t.Cleanup(client.Stop)

	// The replacement comes up while the client is still trying.
	time.Sleep(150 * time.Millisecond)
	replacement := runDaemon(t, path, backend)
	replacement.waitAttached(t, 1)

	select {
	case err := <-ended:
		t.Fatalf("Start gave up instead of reattaching (err=%v) — every stream is now "+
			"closed and no listen command will re-arm", err)
	default:
	}

	// The UI's original channel still delivers.
	feed <- domain.Message{ID: mx + "$after", RoomID: mx + "!r:x", Body: "after the race"}
	if got := nextMessage(t, client.Messages()); got.ID != mx+"$after" {
		t.Fatalf("message after the race = %+v, want $after", got)
	}
}

// A daemon that answers Status but ends every stream at once is not reattached every
// 250ms forever: the backoff keeps climbing.
func TestStreamsThatEndAtOnceBackOff(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	ended := make(chan domain.Message)
	close(ended) // the first stream ends as it opens, taking the rest with it
	runDaemon(t, path, apitest.Nop{Msgs: ended})

	client := daemon.NewRemote(path)
	go func() { _ = client.Start(context.Background()) }()
	t.Cleanup(client.Stop)

	reattached := 0
	window := time.After(2500 * time.Millisecond)
	for {
		select {
		case up := <-client.Attached():
			if up {
				reattached++
			}
			continue
		case <-window:
		}
		break
	}
	// 250ms, 500ms, 1s, 2s: at most three reattaches fit; resetting gave about nine.
	if reattached > 4 {
		t.Errorf("reattached %d times in 2.5s, want the backoff to climb", reattached)
	}
}

// cutProxy forwards a socket path to a daemon's and can cut every connection at once,
// as a crash does: streams end in an error, not cleanly.
type cutProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newCutProxy(t *testing.T, path, target string) *cutProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	p := &cutProxy{ln: ln}
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			out, err := (&net.Dialer{}).DialContext(context.Background(), "unix", target)
			if err != nil {
				_ = in.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, in, out)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(out, in); _ = out.Close() }()
			go func() { _, _ = io.Copy(in, out); _ = in.Close() }()
		}
	}()
	t.Cleanup(p.cut)
	return p
}

// cut closes the listener and every connection abruptly.
func (p *cutProxy) cut() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

// A client that was once attached survives a crash and keeps trying however long the
// restart takes; only a never-attached client gives up.
func TestAClientAttachedOnceOutlastsACrashAndASlowRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, inner := filepath.Join(dir, "kithd.sock"), filepath.Join(dir, "inner.sock")
	feed := make(chan domain.Message, 4)
	backend := apitest.Nop{Msgs: feed}

	first := runDaemon(t, inner, backend)
	proxy := newCutProxy(t, path, inner)
	client := daemon.NewRemote(path)
	ended := make(chan error, 1)
	go func() { ended <- client.Start(context.Background()) }()
	t.Cleanup(client.Stop)
	first.waitAttached(t, 1)
	feed <- domain.Message{ID: mx + "$before", RoomID: mx + "!r:x", Body: "before the crash"}
	if got := nextMessage(t, client.Messages()); got.ID != mx+"$before" {
		t.Fatalf("first message = %+v, want $before", got)
	}

	proxy.cut()
	first.stop(t)
	_ = os.Remove(path)
	// Down longer than a never-attached client waits.
	select {
	case err := <-ended:
		t.Fatalf("the client gave up while the daemon was down (err=%v)", err)
	case <-time.After(3 * time.Second):
	}

	second := runDaemon(t, path, backend)
	second.waitAttached(t, 1)
	feed <- domain.Message{ID: mx + "$after", RoomID: mx + "!r:x", Body: "after the restart"}
	if got := nextMessage(t, client.Messages()); got.ID != mx+"$after" {
		t.Fatalf("message after the restart = %+v, want $after", got)
	}
}
