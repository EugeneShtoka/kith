package daemon_test

// A daemon restarting under an attached client.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// The client comes back by itself, and the channel the UI reads never closed.
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
	feed <- domain.Message{ID: "$before", RoomID: "!r:x", Body: "before the restart"}
	if got := nextMessage(t, client.Messages()); got.ID != "$before" {
		t.Fatalf("first message = %+v, want $before", got)
	}

	// The daemon goes away — and the socket file with it, the way a restart leaves it:
	// systemd stops the unit, the listener closes, and the next start binds the path again.
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

	// The point of all of it: a message sent after the restart arrives on the same
	// channel the UI has been holding since startup.
	feed <- domain.Message{ID: "$after", RoomID: "!r:x", Body: "after the restart"}
	if got := nextMessage(t, client.Messages()); got.ID != "$after" {
		t.Fatalf("message after the restart = %+v, want $after", got)
	}

	// And Stop still ends it — a retry loop that outlived the client would keep a
	// goroutine and a socket probe running after quit.
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

// daemonRun is one lifetime of a daemon on a fixed path, so a test can end it and
// start another where it was.
type daemonRun struct {
	streams *daemon.Streams
	served  chan error
	pumped  chan struct{}
	cancel  context.CancelFunc
	stopped bool
}

// runDaemon serves b on path until the test stops it (or ends).
func runDaemon(t *testing.T, path string, b api.Backend) *daemonRun {
	t.Helper()

	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	streams := daemon.NewStreams()
	notifications, err := daemon.NewNotifications(
		config.Config{Notifications: config.Notifications{Enabled: false}}, scopeOnly{b}, me,
		func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		cancel()
		t.Fatalf("NewNotifications: %v", err)
	}
	run := &daemonRun{
		streams: streams, cancel: cancel,
		served: make(chan error, 1), pumped: make(chan struct{}),
	}
	go func() { defer close(run.pumped); streams.Run(ctx, b) }()
	go func() {
		run.served <- daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: b, Streams: streams, State: daemon.NewState(),
			Notifications: notifications, Reload: (&reloadStub{}).fn,
		})
	}()
	t.Cleanup(func() { run.stop(t) })
	return run
}

// stop ends this daemon and waits for it to be gone, so the next one can bind the
// path. Idempotent: the test stops it explicitly and the cleanup stops it again.
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

// nextMessage reads one streamed message, failing the test rather than hanging.
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

// waitAttachment reads one attachment change. The reconnect is on a real clock —
// it backs off and probes the socket — so this waits rather than polls.
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

// A daemon that goes away between the readiness probe and the first subscribe does not
// leave the client permanently stream-less.
func TestAFirstAttachThatRacesARestartStillReattaches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	feed := make(chan domain.Message, 4)
	backend := apitest.Nop{Msgs: feed}

	// The daemon the client was told about, already gone by the time it subscribes.
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

	// The point: the channel the UI has been holding since startup still delivers.
	feed <- domain.Message{ID: "$after", RoomID: "!r:x", Body: "after the race"}
	if got := nextMessage(t, client.Messages()); got.ID != "$after" {
		t.Fatalf("message after the race = %+v, want $after", got)
	}
}

// A daemon that answers Status but ends every stream at once (its sync failed for
// good) is not reattached to every 250ms forever: the backoff keeps climbing.
func TestStreamsThatEndAtOnceBackOff(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	ended := make(chan domain.Message)
	close(ended) // the first stream ends as soon as it opens, taking the rest with it
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
	// 250ms, 500ms, 1s, 2s: at most three reattaches fit. Resetting after each
	// reattached about nine times.
	if reattached > 4 {
		t.Errorf("reattached %d times in 2.5s, want the backoff to climb", reattached)
	}
}
