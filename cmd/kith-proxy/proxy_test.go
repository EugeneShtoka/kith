package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/clientstore"
)

// settle is how long the store may take to catch up with the bridge: a profile's
// answers are read refreshAfter after the frame that dirtied them, and a failed read
// is retried retryAfter later.
const settle = 30 * time.Second

// startWorld is a bridge with profiles Home and Work: Home holds rooms !a and !b,
// Work !b and !c, and each room has said something.
func startWorld() *fakeBridge {
	f := newFakeBridge(map[string][]string{"Home": {"!a", "!b"}, "Work": {"!b", "!c"}}, "Home", "Work")
	for _, room := range []string{"!a", "!b", "!c"} {
		f.say(room, "hello "+room)
	}
	return f
}

// direct is a client of the bridge itself, to read what the store should equal.
func direct(r *reachable) backendv1connect.BackendServiceClient {
	up := r.upstream()
	return backendv1connect.NewBackendServiceClient(up.Client, "http://kithd")
}

// kept is each kept call a test compares, asked in profile through c.
var kept = map[string]func(ctx context.Context, c backendv1connect.BackendServiceClient, profile string) (proto.Message, error){
	"Rooms": func(ctx context.Context, c backendv1connect.BackendServiceClient, p string) (proto.Message, error) {
		r, err := c.Rooms(ctx, inProfile(&v1.RoomsRequest{}, p))
		return msgOf(r, err)
	},
	"Rail": func(ctx context.Context, c backendv1connect.BackendServiceClient, p string) (proto.Message, error) {
		r, err := c.Rail(ctx, inProfile(&v1.RailRequest{}, p))
		return msgOf(r, err)
	},
	"CachedUnread": func(ctx context.Context, c backendv1connect.BackendServiceClient, p string) (proto.Message, error) {
		r, err := c.CachedUnread(ctx, inProfile(&v1.CachedUnreadRequest{}, p))
		return msgOf(r, err)
	},
	"LastMessages": func(ctx context.Context, c backendv1connect.BackendServiceClient, p string) (proto.Message, error) {
		r, err := c.LastMessages(ctx, inProfile(&v1.LastMessagesRequest{}, p))
		return msgOf(r, err)
	},
	"GetConfig": func(ctx context.Context, c backendv1connect.BackendServiceClient, p string) (proto.Message, error) {
		r, err := c.GetConfig(ctx, inProfile(&v1.GetConfigRequest{}, p))
		return msgOf(r, err)
	},
}

func msgOf[T any, PT interface {
	*T
	proto.Message
}](r *connect.Response[T], err error) (proto.Message, error) {
	if err != nil {
		return nil, err
	}
	return PT(r.Msg), nil
}

// sameAsBridge is nil once every kept answer and every held room's newest messages,
// read from the store held, equal the bridge's own, in every profile.
func sameAsBridge(ctx context.Context, f *fakeBridge, r *reachable, proxied backendv1connect.BackendServiceClient) error {
	bridge := direct(r)
	for _, p := range f.profiles {
		for name, call := range kept {
			want, err := call(ctx, bridge, p)
			if err != nil {
				return fmt.Errorf("the bridge's %s in %s: %w", name, p, err)
			}
			got, err := call(ctx, proxied, p)
			if err != nil {
				return fmt.Errorf("the store's %s in %s: %w", name, p, err)
			}
			if !proto.Equal(got, want) {
				return fmt.Errorf("the store's %s in %s = %v, the bridge's %v", name, p, got, want)
			}
		}
		set, err := f.set(inProfile(&v1.RoomsRequest{}, p).Header())
		if err != nil {
			return err
		}
		for _, room := range set {
			want, err := bridge.CachedTimeline(ctx, inProfile(&v1.CachedTimelineRequest{RoomId: room}, p))
			if err != nil {
				return fmt.Errorf("the bridge's %s: %w", room, err)
			}
			got, err := proxied.CachedTimeline(ctx, inProfile(&v1.CachedTimelineRequest{RoomId: room}, p))
			if err != nil {
				return fmt.Errorf("the store's %s in %s: %w", room, p, err)
			}
			if !proto.Equal(got.Msg, want.Msg) {
				return fmt.Errorf("the store's %s in %s = %v, the bridge's %v", room, p, got.Msg, want.Msg)
			}
		}
	}
	return nil
}

// event is one thing that happens to the bridge or the proxy.
type event struct {
	name string
	do   func(t *testing.T, w *world)
}

// world is one run: the bridge, its reach, and the proxy over its store.
type world struct {
	f     *fakeBridge
	r     *reachable
	sp    *servedProxy
	view  backendv1connect.BackendServiceClient
	path  string
	key   []byte
	store *clientstore.Store
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{f: startWorld(), path: filepath.Join(t.TempDir(), "proxy.db"), key: newKey(t)}
	w.r = serveFake(t, w.f)
	w.store = openTestStore(t, w.path, w.key)
	w.view = heldView(t, w.store)
	w.sp = serveProxyOf(t, w.r, w.store, w.path, true)
	t.Cleanup(func() { w.sp.stop(); _ = w.store.Close() })
	return w
}

// restart stops the proxy and starts it again over the same file, as a deploy does.
func (w *world) restart(t *testing.T) {
	t.Helper()
	w.sp.stop()
	if err := w.store.Close(); err != nil {
		t.Fatal(err)
	}
	w.store = openTestStore(t, w.path, w.key)
	w.view = heldView(t, w.store)
	w.sp = serveProxyOf(t, w.r, w.store, w.path, true)
}

// crossPage has room's history filled again, with an edit made after the bridge read
// the page and streamed to the store before the page reaches it: the page is older
// word than the row the stream wrote.
func (w *world) crossPage(t *testing.T, room string) {
	t.Helper()
	body := "edited while its page was on the way"
	done := make(chan struct{})
	var once sync.Once
	w.f.mu.Lock()
	w.f.onTimeline = func(r string) {
		if r != room {
			return
		}
		once.Do(func() {
			defer close(done)
			w.f.edit(room, body)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && !w.holds(room, body) {
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	w.f.mu.Unlock()
	if err := w.store.Unfill(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(settle):
		t.Fatal("no page of " + room + " was asked")
	}
	w.f.mu.Lock()
	w.f.onTimeline = nil
	w.f.mu.Unlock()
}

// holds reports whether the store holds a message of room saying body.
func (w *world) holds(room, body string) bool {
	rows, err := w.store.Timeline(context.Background(), room, timelineLimit)
	if err != nil {
		return false
	}
	for _, b := range rows {
		var m v1.Message
		if proto.Unmarshal(b, &m) == nil && m.GetBody() == body {
			return true
		}
	}
	return false
}

// events is what the generator orders: every kind of change the store copies, and
// the proxy's own restarts and the bridge going out of reach and back, across which
// it must resume.
var events = []event{
	{"a message in !a", func(_ *testing.T, w *world) { w.f.say("!a", "new in a") }},
	{"a message in !c", func(_ *testing.T, w *world) { w.f.say("!c", "new in c") }},
	{"a message of my own in !b, from another device", func(_ *testing.T, w *world) { w.f.post("!b", "mine") }},
	{"an edit in !b", func(_ *testing.T, w *world) { w.f.edit("!b", "edited b") }},
	{"!a moves from Home to Work", func(_ *testing.T, w *world) { w.f.move("!a", "Home", "Work") }},
	{"!b leaves Work", func(_ *testing.T, w *world) { w.f.move("!b", "Work", "Home") }},
	{"Home's rail changes", func(_ *testing.T, w *world) { w.f.bumpRail("Home") }},
	{"!a is read", func(_ *testing.T, w *world) { w.f.read("!a") }},
	{"the proxy restarts", func(t *testing.T, w *world) { w.restart(t) }},
	{"a message while the proxy is stopped", func(t *testing.T, w *world) {
		w.sp.stop()
		w.f.say("!a", "said while the proxy was stopped")
		w.restart(t)
	}},
	{"the bridge's cache is cleared", func(_ *testing.T, w *world) { w.f.clear("!c", "c as the network gave it again") }},
	{"a page of !b and an edit in it cross", func(t *testing.T, w *world) { w.crossPage(t, "!b") }},
	{"a message while the bridge is out of reach", func(_ *testing.T, w *world) {
		w.r.setDown(true)
		w.f.say("!b", "said while away")
		time.Sleep(200 * time.Millisecond)
		w.r.setDown(false)
	}},
}

// Caches a derived result: from a settled start, every ordered pair of events (each
// change the store copies, a restart, an outage), with the store read held once
// settled again: every kept answer and every held room's messages, in each profile,
// equal the bridge's answer to the same call.
func TestTheStoreAnswersAsTheBridgeOnceSettled(t *testing.T) {
	t.Parallel()
	for _, first := range events {
		for _, second := range events {
			t.Run(first.name+", then "+second.name, func(t *testing.T) {
				t.Parallel()
				w := newWorld(t)
				eventually(t, settle, "the store settled at the start", func() error {
					return heldSame(t.Context(), w)
				})
				first.do(t, w)
				second.do(t, w)
				eventually(t, settle, "the store settled after the events", func() error {
					return heldSame(t.Context(), w)
				})
			})
		}
	}
}

// heldSame compares what the store answers held with the bridge. A room still to fill
// is not settled: a page on its way may yet write over a row.
func heldSame(ctx context.Context, w *world) error {
	if rooms, err := w.store.Unfilled(ctx); err != nil || len(rooms) > 0 {
		return fmt.Errorf("rooms still to fill: %v %w", rooms, err)
	}
	return sameAsBridge(ctx, w.f, w.r, w.view)
}

// Enforces scope: held, a profile is answered only from the rooms its last set holds,
// whatever its header says (another profile, another case, none, one the bridge does
// not have); and a room leaving a profile leaves it offline too.
func TestHeldAnswersKeepToTheProfile(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	eventually(t, settle, "the store settled", func() error { return heldSame(t.Context(), w) })
	c := w.view
	cases := []struct {
		header string
		admits []string // of !a, !b, !c, !never
		err    connect.Code
	}{
		{header: "Home", admits: []string{"!a", "!b"}},
		{header: "home", admits: []string{"!a", "!b"}},
		{header: "Work", admits: []string{"!b", "!c"}},
		{header: "", admits: []string{"!a", "!b", "!c", "!never"}},
		{header: "Nobody", err: connect.CodeInternal},
	}
	for _, tc := range cases {
		for _, room := range []string{"!a", "!b", "!c", "!never"} {
			_, err := c.CachedTimeline(t.Context(), inProfile(&v1.CachedTimelineRequest{RoomId: room}, tc.header))
			_, rerr := c.CachedReactions(t.Context(), inProfile(&v1.CachedReactionsRequest{RoomId: room}, tc.header))
			switch {
			case tc.err != 0:
				if connect.CodeOf(err) != tc.err || connect.CodeOf(rerr) != tc.err {
					t.Errorf("%q asking %s = %v, %v; want %v", tc.header, room, err, rerr, tc.err)
				}
				var ce *connect.Error
				if errors.As(err, &ce) && ce.Meta().Get(sentinelHeader) != unknownProfile {
					t.Errorf("%q asking %s is not tagged an unknown profile: %v", tc.header, room, ce.Meta())
				}
			case slices.Contains(tc.admits, room):
				if err != nil {
					t.Errorf("CachedTimeline of %s in %q = %v, want it answered", room, tc.header, err)
				}
				if rerr != nil && room != "!never" {
					t.Errorf("CachedReactions of %s in %q = %v, want it answered", room, tc.header, rerr)
				}
			default:
				if connect.CodeOf(err) != connect.CodeNotFound || connect.CodeOf(rerr) != connect.CodeNotFound {
					t.Errorf("%s in %q = %v, %v; want not found", room, tc.header, err, rerr)
				}
			}
		}
	}
	for _, tc := range []struct {
		header string
		rooms  []string
	}{{"Work", []string{"!b", "!c"}}, {"", []string{"!a", "!b", "!c"}}} {
		if got := heldRooms(t, c, tc.header); !slices.Equal(got, tc.rooms) {
			t.Errorf("the held Messages stream in %q carried %v, want %v", tc.header, got, tc.rooms)
		}
	}

	w.f.move("!a", "Home", "Work")
	eventually(t, settle, "the store settled after !a left Home", func() error { return heldSame(t.Context(), w) })
	if _, err := c.CachedTimeline(t.Context(), inProfile(&v1.CachedTimelineRequest{RoomId: "!a"}, "Home")); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("!a in Home after it left = %v, want not found", err)
	}
}

// heldRooms is the rooms whose messages a held Messages stream in profile carries from
// the store's start.
func heldRooms(t *testing.T, c backendv1connect.BackendServiceClient, profile string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	st, err := c.Messages(ctx, inProfile(&v1.MessagesRequest{}, profile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if !st.Receive() {
		t.Fatalf("the held stream in %q said nothing: %v", profile, st.Err())
	}
	start := st.Msg()
	// From the store's numbering at 0, every message it holds comes.
	st2, err := c.Messages(ctx, inProfile(&v1.MessagesRequest{SinceEpoch: start.GetEpoch(), SinceChange: 0}, profile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	var rooms []string
	if !st2.Receive() || st2.Msg().GetReadAgain() {
		t.Fatalf("the held stream in %q would not resume from 0: %v", profile, st2.Err())
	}
	for st2.Receive() {
		if room := st2.Msg().GetMessage().GetRoomId(); room != "" && !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	slices.Sort(rooms)
	return rooms
}

// Concurrent: a client's stream forwarded ends once the bridge has been out of reach
// for heldAfter, and reopened it is told to read again from the store, which answers;
// once the bridge has answered for liveAfter the held stream ends, and reopened it is
// forwarded and told to read again from the bridge.
func TestAClientIsSwitchedBetweenTheBridgeAndTheStore(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	eventually(t, settle, "the store settled", func() error { return heldSame(t.Context(), w) })
	c := w.sp.client()
	st, err := c.Messages(t.Context(), connect.NewRequest(&v1.MessagesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Receive() || st.Msg().GetEpoch() != w.f.epoch {
		t.Fatalf("the forwarded stream's first frame = %v, %v", st.Msg(), st.Err())
	}
	bridgeAt := st.Msg().GetChange()

	w.r.setDown(true)
	ended := time.Now()
	for st.Receive() {
	}
	_ = st.Close()
	held, err := openResumed(t, c, w.f.epoch, bridgeAt, heldAfter+5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(ended); took < heldAfter-2*time.Second {
		t.Errorf("held after %s out of reach, want about %s", took, heldAfter)
	}
	first := held.Msg()
	if !first.GetReadAgain() || first.GetEpoch() != w.store.Epoch() {
		t.Errorf("the held stream's first frame = %v, want read again in the store's epoch %s", first, w.store.Epoch())
	}
	status, err := c.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if since := status.Msg.GetHeldSince().AsTime(); status.Msg.GetHeldSince() == nil ||
		since.After(ended) || ended.Sub(since) > probeEvery+probeWait {
		t.Errorf("held Status's held_since = %v, want when the bridge last answered, just before %v", status.Msg.GetHeldSince(), ended)
	}
	if _, ferr := c.CachedTimeline(t.Context(), connect.NewRequest(&v1.CachedTimelineRequest{RoomId: "!a"})); ferr != nil {
		t.Errorf("CachedTimeline held = %v", ferr)
	}
	if _, ferr := c.Send(t.Context(), connect.NewRequest(&v1.SendRequest{RoomId: "!a"})); connect.CodeOf(ferr) != connect.CodeUnavailable {
		t.Errorf("Send held = %v, want unavailable", ferr)
	}

	w.r.setDown(false)
	back := time.Now()
	for held.Receive() {
	}
	_ = held.Close()
	if took := time.Since(back); took < liveAfter-time.Second || took > liveAfter+5*time.Second {
		t.Errorf("live after the bridge answered for %s, want about %s", took, liveAfter)
	}
	live, err := openResumed(t, c, first.GetEpoch(), first.GetChange(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := c.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil || status.Msg.GetHeldSince() != nil {
		t.Errorf("Status live = %v, %v; want no held_since", status, err)
	}
	if m := live.Msg(); !m.GetReadAgain() || m.GetEpoch() != w.f.epoch {
		t.Errorf("the stream reopened live = %v, want read again in the bridge's epoch", m)
	}
	_ = live.Close()
}

// openResumed opens Messages from epoch and change until it answers, within d, and
// returns it having read its first frame.
func openResumed(t *testing.T, c backendv1connect.BackendServiceClient, epoch string, change int64, d time.Duration) (*connect.ServerStreamForClient[v1.MessagesResponse], error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := c.Messages(t.Context(), connect.NewRequest(&v1.MessagesRequest{SinceEpoch: epoch, SinceChange: change}))
		if err == nil {
			if st.Receive() {
				return st, nil
			}
			_ = st.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("no stream from %s/%d answered within %s", epoch, change, d)
}

// A proxy started with the bridge out of reach answers from its store at once, and
// says as of when it last heard the bridge, before it went out of reach.
func TestAProxyStartedOfflineSaysHowCurrentItsStoreIs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	eventually(t, settle, "the store settled", func() error { return heldSame(t.Context(), w) })
	w.sp.stop()
	heard := w.sp.p.state.heardAt()
	w.r.setDown(true)
	sp := serveProxyOf(t, w.r, w.store, w.path, true)
	if held, _ := sp.p.state.now(); !held {
		t.Fatal("a proxy started offline over a store that holds a profile is not held")
	}
	status, err := sp.client().Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := status.Msg.GetHeldSince().AsTime(); status.Msg.GetHeldSince() == nil || got.After(heard) || heard.Sub(got) > keepHeardEvery {
		t.Errorf("held_since after a restart offline = %v, want within %s before %v", status.Msg.GetHeldSince(), keepHeardEvery, heard)
	}
}

// Held or live by the bridge's answers: held only once it has not answered for
// heldAfter, live only once it has answered throughout liveAfter; a failure between
// starts the wait for live again.
func TestHysteresis(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	at := func(s float64) time.Time { return start.Add(time.Duration(s * float64(time.Second))) }
	for _, tc := range []struct {
		name  string
		held  bool
		probe []bool // one a second from 0
		want  []bool
	}{
		{"a short drop stays live", false,
			[]bool{true, false, false, false, true},
			[]bool{false, false, false, false, false}},
		{"an outage goes held after heldAfter", false,
			[]bool{true, false, false, false, false, false, false, false, false, false, false, false},
			[]bool{false, false, false, false, false, false, false, false, false, false, true, true}},
		{"answers go live after liveAfter", true,
			[]bool{true, true, true, true, true, true, true},
			[]bool{true, true, true, true, true, false, false}},
		{"a failure restarts the wait for live", true,
			[]bool{true, true, true, false, true, true, true, true, true, true},
			[]bool{true, true, true, true, true, true, true, true, true, false}},
	} {
		var h hysteresis
		held := tc.held
		var got []bool
		for i, ok := range tc.probe {
			held = h.heard(ok, at(float64(i)), held)
			got = append(got, held)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: held = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A bridge refusing this device's key leaves nothing of it here: the store's file is
// deleted, and the proxy forwards from then on, so the refusal reaches every client.
func TestARefusalDeletesTheStore(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	eventually(t, settle, "the store settled", func() error { return heldSame(t.Context(), w) })
	w.sp.stop()
	sp := serveProxyOf(t, w.r, w.store, w.path, false)
	sp.p.state.set(true, time.Now())
	sp.p.refused(func() {})
	if _, err := os.Stat(w.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the store after a refusal: %v, want it deleted", err)
	}
	if held, _ := sp.p.state.now(); held {
		t.Error("held after a refusal, want forwarding")
	}
	if _, err := sp.client().Rooms(t.Context(), connect.NewRequest(&v1.RoomsRequest{})); err != nil {
		t.Errorf("Rooms forwarded after a refusal = %v", err)
	}
}
