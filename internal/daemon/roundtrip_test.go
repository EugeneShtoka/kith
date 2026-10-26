package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// settle bounds every wait in these tests.
const settle = 5 * time.Second

// fakeBackend is apitest.Nop with a room list and test-fed channels.
type fakeBackend struct {
	apitest.Nop
	rooms []domain.Room
	err   error
}

func (f *fakeBackend) Rooms(context.Context) ([]domain.Room, error) {
	return f.rooms, f.err
}

// harness is a daemon on a temporary socket plus the fan-out feeding it.
type harness struct {
	t             *testing.T
	path          string
	streams       *daemon.Streams
	state         *daemon.State
	notifications *daemon.Notifications
	reload        *reloadStub
}

// reloadStub stands in for the config re-read: it counts calls and can refuse.
type reloadStub struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *reloadStub) fn(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *reloadStub) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *reloadStub) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// scopeOnly adds what the notifier needs beyond api.Backend: whether we spoke in a thread.
type scopeOnly struct{ api.Backend }

func (scopeOnly) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool {
	return false
}

// serve starts a daemon on a temporary socket, pumping b's channels into its fan-out;
// shutdown and exit are checked at cleanup.
func serve(t *testing.T, b api.Backend, with ...func(*daemon.Daemon)) *harness {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	streams := daemon.NewStreams()
	state := daemon.NewState()
	notifications, err := daemon.NewNotifications(asStarted(notifsOn("all")), scopeOnly{b},
		func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	notifications.UseSelves(func() []string { return []string{me} })
	notifications.Synced(mx, time.Now()) // past the catch-up batch, so a message notifies
	notifications.Synced(mx, time.Now())
	reload := &reloadStub{}
	pumped := make(chan struct{})
	go func() { defer close(pumped); streams.Run(ctx, b) }()
	served := make(chan error, 1)
	d := &daemon.Daemon{
		Backend: b, Streams: streams, State: state,
		Notifications: notifications, Reload: reload.fn,
	}
	for _, f := range with {
		f(d)
	}
	complete(t, d)
	go func() { served <- daemon.Serve(ctx, ln, d) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v, want nil", err)
		}
		<-pumped
	})
	return &harness{
		t: t, path: path, streams: streams, state: state,
		notifications: notifications, reload: reload,
	}
}

func (h *harness) client() *daemon.Remote {
	h.t.Helper()
	return daemon.NewRemote(h.path)
}

// waitAttached blocks until want clients have subscribed to the daemon's streams.
func (h *harness) waitAttached(want int) {
	h.t.Helper()

	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if h.streams.Attached() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %d client(s) to attach; %d attached", want, h.streams.Attached())
}

// attach is serve plus one client.
func attach(t *testing.T, b api.Backend) (*daemon.Remote, *harness) {
	t.Helper()

	h := serve(t, b)
	r := h.client()
	return r, h
}

// The TUI sees the daemon's rooms exactly as the in-process backend returns them.
func TestRoomsRoundTrip(t *testing.T) {
	t.Parallel()

	want := []domain.Room{
		{ID: mx + "!general:example.org", Name: "General", Members: []string{"Ada", "Grace"}},
		{ID: mx + "!dm:example.org", Name: "Ada", IsDirect: true, Members: []string{"Ada"}},
		{ID: mx + "!invited:example.org", Name: "Book club", Membership: domain.MembershipInvite, InvitedBy: "matrix:@ada:example.org"},
	}
	r, _ := attach(t, &fakeBackend{rooms: want})

	got, err := r.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Rooms() = %+v, want %+v", got, want)
	}
}

// A backend failure reaches the client with the daemon's message, which the TUI shows.
func TestRoomsPropagatesBackendError(t *testing.T) {
	t.Parallel()

	r, _ := attach(t, &fakeBackend{err: errors.New("cache is locked")})

	_, err := r.Rooms(context.Background())
	if err == nil {
		t.Fatal("Rooms() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "cache is locked") {
		t.Errorf("Rooms() error = %q, want it to mention the daemon's message", err)
	}
}

// A canceled caller context fails the call rather than hanging.
func TestRoomsHonorsCallerContext(t *testing.T) {
	t.Parallel()

	r, _ := attach(t, &fakeBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Rooms(ctx); err == nil {
		t.Error("Rooms() error = nil, want a context error")
	}
}

// Messages cross intact and Stop ends the subscription cleanly.
func TestMessagesStreamRoundTrip(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message, 1)
	r, h := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})

	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	h.waitAttached(1)

	want := domain.Message{
		ID: mx + "$one:example.org", RoomID: mx + "!general:example.org",
		Sender: "matrix:@ada:example.org", SenderName: "Ada", Body: "hello",
		Timestamp: time.UnixMilli(time.Now().UnixMilli()),
		Mentioned: true,
		Mentions:  []domain.Mention{{UserID: "matrix:@grace:example.org", Name: "Grace"}},
	}
	msgs <- want

	select {
	case got := <-r.Messages():
		if !got.Timestamp.Equal(want.Timestamp) {
			t.Errorf("Timestamp = %v, want %v", got.Timestamp, want.Timestamp)
		}
		got.Timestamp, want.Timestamp = time.Time{}, time.Time{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("streamed message = %+v, want %+v", got, want)
		}
	case <-time.After(settle):
		t.Fatal("timed out waiting for the streamed message")
	}

	r.Stop()
	select {
	case err := <-started:
		if err != nil {
			t.Errorf("Start() = %v, want nil after Stop", err)
		}
	case <-time.After(settle):
		t.Fatal("timed out waiting for Start to return after Stop")
	}
	if _, ok := <-r.Messages(); ok {
		t.Error("Messages() still open after Start returned, want closed")
	}
}

// The daemon ending its streams is a reconnect: the client's channels stay open until
// Stop.
func TestTheDaemonEndingAStreamIsNotTheEndOfTheClient(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message)
	r, _ := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})

	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	close(msgs) // the daemon's source ends, ending every stream

	// It reports the loss, then the return: the daemon is still serving.
	if got := waitAttachment(t, r.Attached()); got {
		t.Error("Attached() = true when the streams ended, want false")
	}
	if got := waitAttachment(t, r.Attached()); !got {
		t.Error("Attached() = false after re-subscribing, want true")
	}
	select {
	case err := <-started:
		t.Fatalf("Start() = %v; the client gave up on a daemon that is still there", err)
	default:
	}
	// The UI's channel is still the same.
	select {
	case _, open := <-r.Messages():
		if !open {
			t.Error("Messages() closed on a stream end, want it open across the reconnect")
		}
	default:
	}

	// Only Stop finishes it.
	r.Stop()
	select {
	case err := <-started:
		if err != nil {
			t.Errorf("Start() = %v, want nil after Stop", err)
		}
	case <-time.After(settle):
		t.Fatal("timed out waiting for Start to return")
	}
	if _, ok := <-r.Messages(); ok {
		t.Error("Messages() still open after Stop, want closed")
	}
}

// Stop before Start must not be lost, or a client that gave up would attach anyway.
func TestStopBeforeStart(t *testing.T) {
	t.Parallel()

	r, _ := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: make(chan domain.Message)}})
	r.Stop()
	r.Stop() // idempotent

	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() = %v, want nil after Stop", err)
		}
	case <-time.After(settle):
		t.Fatal("Start blocked after Stop")
	}
	if _, ok := <-r.Messages(); ok {
		t.Error("Messages() open after Stop, want closed")
	}
}

// Dialing a socket nobody listens on fails at the call, not at construction.
func TestRemoteWithNoDaemon(t *testing.T) {
	t.Parallel()

	r := daemon.NewRemote(filepath.Join(t.TempDir(), "absent.sock"))
	if _, err := r.Rooms(context.Background()); err == nil {
		t.Error("Rooms() error = nil, want a dial failure")
	}
	if err := r.Start(context.Background()); err == nil {
		t.Error("Start() = nil, want a dial failure")
	}
}

// A second Start is refused rather than panicking.
func TestStartTwice(t *testing.T) {
	t.Parallel()

	r, _ := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: make(chan domain.Message)}})

	// End the first attachment with its context, not Stop: a stopped Remote attaches
	// nothing.
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- r.Start(ctx) }()
	cancel()
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first Start() = %v, want nil", err)
		}
	case <-time.After(settle):
		t.Fatal("timed out waiting for the first Start to return")
	}

	if err := r.Start(context.Background()); err == nil {
		t.Error("second Start() = nil, want an error")
	}
}
