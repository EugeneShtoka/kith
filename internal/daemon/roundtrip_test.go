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

// fakeBackend is the daemon's backend under test: apitest.Nop with a room list to
// read and whatever channels a test feeds.
type fakeBackend struct {
	apitest.Nop
	rooms []domain.Room
	err   error
}

func (f *fakeBackend) Rooms(context.Context) ([]domain.Room, error) {
	return f.rooms, f.err
}

// harness is a daemon running on a temporary socket, plus the fan-out feeding it.
type harness struct {
	t             *testing.T
	path          string
	streams       *daemon.Streams
	state         *daemon.State
	notifications *daemon.Notifications
	reload        *reloadStub
}

// reloadStub stands in for the daemon binary's config re-read: it counts calls and can be
// told to refuse.
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

// scopeOnly lends an api.Backend the one thing the notifier needs and the client's
// interface does not carry: whether we have spoken in a thread.
type scopeOnly struct{ api.Backend }

func (scopeOnly) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool {
	return false
}

// serve starts a daemon on a temporary socket, pumping b's channels into its
// fan-out. The server is shut down, and its exit checked, when the test ends.
func serve(t *testing.T, b api.Backend) *harness {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	streams := daemon.NewStreams()
	state := daemon.NewState()
	notifications, err := daemon.NewNotifications(notifsOn("all"), scopeOnly{b},
		func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	notifications.UseSelves(func() []string { return []string{me} })
	notifications.Synced(time.Now()) // past the catch-up batch, so a message notifies
	notifications.Synced(time.Now())
	reload := &reloadStub{}
	pumped := make(chan struct{})
	go func() { defer close(pumped); streams.Run(ctx, b) }()
	served := make(chan error, 1)
	go func() {
		served <- daemon.Serve(ctx, ln, &daemon.Daemon{
			Backend: b, Streams: streams, State: state,
			Notifications: notifications, Reload: reload.fn,
		})
	}()
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

// client returns a fresh Remote dialing this daemon.
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

// attach is serve + one client, for the tests that need only that.
func attach(t *testing.T, b api.Backend) (*daemon.Remote, *harness) {
	t.Helper()

	h := serve(t, b)
	r := h.client()
	return r, h
}

// A read method survives the wire: the TUI sees the daemon's rooms exactly as the
// in-process backend would have handed them over.
func TestRoomsRoundTrip(t *testing.T) {
	t.Parallel()

	want := []domain.Room{
		{ID: "!general:example.org", Name: "General", Members: []string{"Ada", "Grace"}},
		{ID: "!dm:example.org", Name: "Ada", IsDirect: true, Members: []string{"Ada"}},
		{ID: "!invited:example.org", Name: "Book club", Membership: domain.MembershipInvite, InvitedBy: "@ada:example.org"},
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

// A backend failure reaches the client as a failure, carrying the daemon's own message —
// the TUI shows that text, so losing it would leave the user with nothing to act on.
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

// A canceled caller context fails the call rather than hanging, which is what
// makes the TUI's context the thing that bounds a request.
func TestRoomsHonorsCallerContext(t *testing.T) {
	t.Parallel()

	r, _ := attach(t, &fakeBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Rooms(ctx); err == nil {
		t.Error("Rooms() error = nil, want a context error")
	}
}

// The stream survives the wire, message content intact, and Stop ends the subscription
// cleanly.
func TestMessagesStreamRoundTrip(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message, 1)
	r, h := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})

	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	h.waitAttached(1)

	want := domain.Message{
		ID: "$one:example.org", RoomID: "!general:example.org",
		Sender: "@ada:example.org", SenderName: "Ada", Body: "hello",
		Timestamp: time.UnixMilli(time.Now().UnixMilli()),
		Mentioned: true,
		Mentions:  []domain.Mention{{UserID: "@grace:example.org", Name: "Grace"}},
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

// The daemon ending its streams is a reconnect, not the end: the client's channels stay
// open until Stop.
func TestTheDaemonEndingAStreamIsNotTheEndOfTheClient(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message)
	r, _ := attach(t, &fakeBackend{Nop: apitest.Nop{Msgs: msgs}})

	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	close(msgs) // the daemon's own source ends, so it ends every stream

	// It says so, and then says it is back — the daemon is still serving, which is
	// what the reconnect probes for.
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
	// And the channel the UI holds is still the channel the UI holds.
	select {
	case _, open := <-r.Messages():
		if !open {
			t.Error("Messages() closed on a stream end, want it open across the reconnect")
		}
	default:
	}

	// Finished is Stop, and only Stop.
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

// Stop before Start must not be lost: otherwise a client that gave up during
// startup would attach anyway and block on a channel nobody reads.
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

// Dialing a socket nobody is listening on fails at the call, not at construction.
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

	// The first attachment ends with its context rather than with Stop, because Stop is the
	// other answer: a stopped Remote attaches nothing and says nothing, which is right for
	// a client that has quit.
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
