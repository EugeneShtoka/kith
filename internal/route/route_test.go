package route

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two networks' rooms, as their IDs name them.
const (
	matrixRoomID   domain.RoomID = mx + "!a:x"
	whatsappRoomID domain.RoomID = "whatsapp:44881234567/120363@g.us"
)

func twoNetworks(t *testing.T) (*Router, *fakeMatrix, *fake) {
	t.Helper()
	m, wa := newFakeMatrix(), newFake("whatsapp")
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolWhatsApp: wa})
	return r, m, wa
}

// perRoomCalls makes every per-room call through the router.
func perRoomCalls(ctx context.Context, r *Router, room domain.RoomID) map[string]error {
	calls := map[string]error{}
	calls["MarkRead"] = r.MarkRead(ctx, room, mx+"$e", false)
	calls["MarkRoomUnread"] = r.MarkRoomUnread(ctx, room, true)
	calls["StarMessage"] = r.StarMessage(ctx, room, mx+"$e", true)
	calls["MarkTag"] = r.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Spam"})
	_, calls["CanonicalParent"] = r.CanonicalParent(ctx, room)
	_, _, calls["MessageHistory"] = r.MessageHistory(ctx, room, mx+"$e")
	_, calls["Timeline"] = r.Timeline(ctx, room, "", 10)
	_, calls["FetchEvent"] = r.FetchEvent(ctx, room, mx+"$e")
	calls["Redact"] = r.Redact(ctx, room, mx+"$e", "")
	calls["Send"] = r.Send(ctx, room, domain.Draft{Body: "hi"})
	calls["SendTyping"] = r.SendTyping(ctx, room, true, time.Second)
	calls["SendFile"] = r.SendFile(ctx, room, "/f", "f", "")
	calls["SendReaction"] = r.SendReaction(ctx, room, mx+"$e", "👍")
	_, calls["LoadImage"] = r.LoadImage(ctx, room, mx+"$e")
	_, calls["Members"] = r.Members(ctx, room, 0)
	_, calls["RefreshMembers"] = r.RefreshMembers(ctx, room)
	_, calls["MentionCandidates"] = r.MentionCandidates(ctx, room, 0)
	return calls
}

// Every call about a room reaches that room's network, and only it.
func TestACallAboutARoomReachesItsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		room domain.RoomID
		to   string
	}{{matrixRoomID, "matrix"}, {whatsappRoomID, "whatsapp"}} {
		r, m, wa := twoNetworks(t)
		calls := perRoomCalls(ctx, r, tc.room)
		for name, err := range calls {
			if err != nil {
				t.Errorf("%s about %s: %v", name, tc.room, err)
			}
		}
		reached, other := m.seen(), wa.seen()
		if tc.to == "whatsapp" {
			reached, other = other, reached
		}
		if len(reached) != len(calls) {
			t.Errorf("%s reached %d of %d calls about %s", tc.to, len(reached), len(calls), tc.room)
		}
		for _, room := range reached {
			if room != tc.room {
				t.Errorf("%s was asked about %s, want %s", tc.to, room, tc.room)
			}
		}
		if len(other) != 0 {
			t.Errorf("the other network was asked about %v", other)
		}
	}
}

// A room on a network with no adapter is refused as such, not sent to Matrix.
func TestARoomOnANetworkNotConnectedIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newFakeMatrix()
	r := routed(m, nil)
	for name, err := range perRoomCalls(ctx, r, whatsappRoomID) {
		if !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("%s = %v, want ErrNetworkOff", name, err)
		}
	}
	if seen := m.seen(); len(seen) != 0 {
		t.Errorf("Matrix was asked about another network's room: %v", seen)
	}
}

// Matrix-only calls are refused for another network's room before Matrix hears of it.
func TestMatrixOnlyCallsStayOnMatrix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	calls := func(r *Router, room domain.RoomID) map[string]error {
		out := map[string]error{}
		out["AddToSpace"] = r.AddToSpace(ctx, mx+"!s:x", room)
		out["RemoveFromSpace"] = r.RemoveFromSpace(ctx, mx+"!s:x", room)
		out["InviteUser"] = r.InviteUser(ctx, room, "matrix:@u:x")
		out["KickUser"] = r.KickUser(ctx, room, "matrix:@u:x", "")
		out["BanUser"] = r.BanUser(ctx, room, "matrix:@u:x", "")
		out["UnbanUser"] = r.UnbanUser(ctx, room, "matrix:@u:x")
		out["LeaveRoom"] = r.LeaveRoom(ctx, room)
		_, out["ListThreads"] = r.ListThreads(ctx, room)
		_, out["ThreadPage"] = r.ThreadPage(ctx, room, mx+"$r", "", 10)
		out["MarkThreadRead"] = r.MarkThreadRead(ctx, room, mx+"$r", mx+"$e", false)
		return out
	}

	r, m, wa := twoNetworks(t)
	for name, err := range calls(r, whatsappRoomID) {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s on a WhatsApp room = %v, want ErrNotOnNetwork", name, err)
		}
	}
	if r.ThreadParticipant(ctx, whatsappRoomID, mx+"$r") {
		t.Error("ThreadParticipant is true for a network without threads")
	}
	if got := m.calls(); len(got) != 0 {
		t.Errorf("Matrix heard of another network's room: %v", got)
	}
	if seen := wa.seen(); len(seen) != 0 {
		t.Errorf("WhatsApp was asked a Matrix-only thing: %v", seen)
	}

	mine := calls(r, matrixRoomID)
	for name, err := range mine {
		if err != nil {
			t.Errorf("%s on a Matrix room: %v", name, err)
		}
	}
	if !r.ThreadParticipant(ctx, matrixRoomID, mx+"$r") {
		t.Error("ThreadParticipant on a Matrix room did not reach Matrix")
	}
	if got := m.calls(); len(got) != len(mine)+1 {
		t.Errorf("Matrix heard %v, want all %d calls", got, len(mine)+1)
	}
}

// Lists join every network's, Matrix first; one failure fails the read.
func TestListsAreEveryNetworksAndFailTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, m, wa := twoNetworks(t)
	m.rooms = []domain.Room{{ID: matrixRoomID}}
	wa.rooms = []domain.Room{{ID: whatsappRoomID}}

	rooms, err := r.Rooms(ctx)
	if err != nil || len(rooms) != 2 || rooms[0].ID != matrixRoomID || rooms[1].ID != whatsappRoomID {
		t.Errorf("Rooms = (%v, %v), want both networks', Matrix first", rooms, err)
	}
	unread, err := r.CachedUnread(ctx)
	if err != nil || len(unread) != 2 {
		t.Errorf("CachedUnread = (%v, %v), want both networks'", unread, err)
	}
	people, err := r.DirectCandidates(ctx, 3)
	if err != nil || len(people) != 3 {
		t.Errorf("DirectCandidates(3) = (%v, %v), want three, across networks", people, err)
	}

	wa.fail = errFake
	if rooms, err := r.Rooms(ctx); !errors.Is(err, errFake) || rooms != nil {
		t.Errorf("Rooms with WhatsApp failing = (%v, %v), want the failure and no partial list", rooms, err)
	}
	if rooms, err := r.RefreshRooms(ctx); !errors.Is(err, errFake) || rooms != nil {
		t.Errorf("RefreshRooms with WhatsApp failing = (%v, %v), want the failure", rooms, err)
	}
}

// With Matrix alone the router passes its streams through.
func TestOneNetworkIsPassedThrough(t *testing.T) {
	t.Parallel()
	m := newFakeMatrix()
	r := routed(m, nil)
	if r.Messages() != (<-chan domain.Message)(m.messages) || r.Unread() != (<-chan domain.Unread)(m.unread) ||
		r.Reactions() != (<-chan domain.ReactionUpdate)(m.reactions) || r.Activity() != (<-chan domain.Activity)(m.activity) {
		t.Error("a lone network's streams were wrapped")
	}
}

// Me is every self on every network, once each.
func TestMeIsEveryNetworksSelf(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	m.me = []string{"matrix:@me:x", "matrix:@whatsapp_me:x"}
	wa.me = []string{"whatsapp:44881234567@s.whatsapp.net", "matrix:@me:x"}
	if got := r.Me(); !slices.Equal(got, []string{"matrix:@me:x", "matrix:@whatsapp_me:x", "whatsapp:44881234567@s.whatsapp.net"}) {
		t.Errorf("Me = %v", got)
	}
}

// Me is kept, not asked of every network on every call: a filter rescan asks it twice
// per cached message, and Signal's answer reads the keyring.
func TestMeIsKeptWhileNothingChanged(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	for range 100 {
		r.Me()
	}
	if got := m.meAsked.Load() + wa.meAsked.Load(); got != 2 {
		t.Errorf("networks asked who you are %d times over 100 calls, want once each", got)
	}
}

// An account's state changing has Me ask again, and returning what it asked.
func TestForgetMeAsksTheNetworksAgain(t *testing.T) {
	t.Parallel()
	r, _, wa := twoNetworks(t)
	r.Me()
	wa.setMe("whatsapp:44881234567@s.whatsapp.net")
	if got := r.Me(); slices.Contains(got, "whatsapp:44881234567@s.whatsapp.net") {
		t.Fatalf("Me = %v before ForgetMe; the test needs it kept", got)
	}
	r.ForgetMe()
	if got := r.Me(); !slices.Contains(got, "whatsapp:44881234567@s.whatsapp.net") {
		t.Errorf("Me after ForgetMe = %v, want the new account in it", got)
	}
}

// A network logging in or out changes Me at once, kept or not: the router sees it
// without a status reaching ForgetMe.
func TestMeFollowsANetworkLoggingInAndOut(t *testing.T) {
	t.Parallel()
	r, m, _ := withoutMatrix(t)["logged out, WhatsApp"]()
	if got := r.Me(); slices.Contains(got, m.account) {
		t.Fatalf("Me = %v while Matrix is logged out", got)
	}
	m.loggedOut.Store(false)
	if got := r.Me(); !slices.Contains(got, m.account) {
		t.Errorf("Me after Matrix logged in = %v, want its account", got)
	}
	m.loggedOut.Store(true)
	if got := r.Me(); slices.Contains(got, m.account) {
		t.Errorf("Me after Matrix logged out = %v, still names it", got)
	}
}

// A network learning an ID without a status (Google Messages, as its inbox fills) is
// heard once meFresh has passed.
func TestMeIsAskedAgainOnceStale(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		r, _, wa := twoNetworks(t)
		r.Me()
		wa.setMe("whatsapp:44881234567@s.whatsapp.net")
		time.Sleep(meFresh)
		if got := r.Me(); !slices.Contains(got, "whatsapp:44881234567@s.whatsapp.net") {
			t.Errorf("Me after meFresh = %v, want the learned ID in it", got)
		}
	})
}

// A ForgetMe landing while Me is being built is not undone by that build: what it
// gathered may predate the change.
func TestForgetMeDuringABuildIsNotLost(t *testing.T) {
	t.Parallel()
	r, _, wa := twoNetworks(t)
	held, entered := make(chan struct{}), make(chan struct{})
	wa.mu.Lock()
	wa.meHeld, wa.meEntered = held, entered
	wa.mu.Unlock()
	built := make(chan []string)
	go func() { built <- r.Me() }()
	<-entered // the build holds WhatsApp's answer from before the account connected
	wa.mu.Lock()
	wa.meHeld = nil
	wa.mu.Unlock()
	wa.setMe("whatsapp:44881234567@s.whatsapp.net")
	r.ForgetMe()
	close(held)
	<-built
	if got := r.Me(); !slices.Contains(got, "whatsapp:44881234567@s.whatsapp.net") {
		t.Errorf("Me = %v, want the account the ForgetMe announced", got)
	}
}

// A cleared cache is refilled by every network.
func TestARewindReachesEveryNetwork(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	wa.fail = errFake
	if err := r.RewindSync(context.Background()); !errors.Is(err, errFake) {
		t.Errorf("RewindSync = %v, want WhatsApp's failure", err)
	}
	if m.rewinds != 1 || wa.rewinds != 1 {
		t.Errorf("rewinds = matrix %d, whatsapp %d; want one each, a failure not stopping the rest", m.rewinds, wa.rewinds)
	}
}

// Every network is optional, Matrix too: with none, Start waits for the end and lists
// are empty.
func TestNewRunsWithNoNetwork(t *testing.T) {
	t.Parallel()
	empty := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- empty.Start(ctx) }()
	select {
	case err := <-started:
		t.Fatalf("Start with no network returned at once (%v), want it running until the end", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := <-started; err != nil {
		t.Errorf("Start with no network ended with %v", err)
	}
	if rooms, err := empty.Rooms(context.Background()); err != nil || len(rooms) != 0 {
		t.Errorf("Rooms with no network = (%v, %v), want none", rooms, err)
	}
}

// randomRooms spreads rooms over Matrix, WhatsApp (when connected), Telegram (a bare
// adapter, when there) and a network with no adapter.
func randomRooms(rng *rand.Rand) []domain.RoomID {
	n := rng.IntN(12)
	out := make([]domain.RoomID, 0, n)
	for i := range n {
		switch rng.IntN(4) {
		case 3:
			out = append(out, domain.RoomID(fmt.Sprintf("telegram:42/-100%d", i)))
		case 0:
			out = append(out, domain.RoomID(fmt.Sprintf("!r%d:x", i)))
		case 1:
			out = append(out, domain.RoomID(fmt.Sprintf("whatsapp:44881234567/%d@g.us", i)))
		default:
			out = append(out, domain.RoomID(fmt.Sprintf("whatsapp:1501234567/%d@g.us", i)))
		}
	}
	return out
}

// Over any mix of networks and refusals, every room is counted once and reaches only
// its own network; an error means nothing was attempted.
func TestMarkingRoomsReadAccountsForEveryRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 1))
		m, wa := newFakeMatrix(), newFake("whatsapp")
		others := map[domain.Protocol]Adapter{}
		connected := rng.IntN(2) == 0
		if connected {
			others[domain.ProtocolWhatsApp] = wa
		}
		if rng.IntN(2) == 0 {
			others[domain.ProtocolTelegram] = bare{newFake("telegram")}
		}
		r := routed(m, others)
		if rng.IntN(3) == 0 {
			m.failMarking = errFake
		}
		if rng.IntN(3) == 0 {
			wa.failMarking = errors.New("whatsapp: refused")
		}
		for _, f := range []*fake{m.fake, wa} {
			f.failEach, f.skipEach = rng.IntN(4), rng.IntN(3)
		}
		rooms := randomRooms(rng)
		if len(rooms) > 0 && rng.IntN(4) == 0 {
			rooms = append(rooms, rooms[rng.IntN(len(rooms))]) // the same room twice
		}

		got, err := r.MarkRoomsRead(ctx, rooms, false)
		where := fmt.Sprintf("seed %d (whatsapp connected %v, rooms %v)", seed, connected, rooms)
		for _, room := range m.marked {
			if domain.NetworkOf(string(room)) != domain.ProtocolMatrix {
				t.Fatalf("%s: Matrix was asked to mark %s", where, room)
			}
		}
		for _, room := range wa.marked {
			if domain.NetworkOf(string(room)) != domain.ProtocolWhatsApp {
				t.Fatalf("%s: WhatsApp was asked to mark %s", where, room)
			}
		}
		if err != nil {
			if len(m.marked)+len(wa.marked) != 0 {
				t.Fatalf("%s: an error came back although rooms were marked", where)
			}
			continue
		}
		if sum := got.Marked + got.Skipped + got.Failed; sum != len(rooms) {
			t.Fatalf("%s: %+v accounts for %d rooms, want %d", where, got, sum, len(rooms))
		}
		if attempted := len(m.marked) + len(wa.marked); got.Marked > attempted {
			t.Fatalf("%s: Marked %d, but only %d were attempted", where, got.Marked, attempted)
		}
		if got.Failed > 0 && got.FirstError == "" {
			t.Fatalf("%s: %d failed with no reason", where, got.Failed)
		}
	}
}

// A person's mark or release reaches the network their ID is on, and only it.
func TestAPersonMarkReachesTheirNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const whatsappPerson = "whatsapp:15550000001@s.whatsapp.net"
	r, m, wa := twoNetworks(t)
	for _, person := range []string{"matrix:@ann:x", whatsappPerson} {
		if err := r.MarkPerson(ctx, domain.PersonVerdict{Person: person, Tag: "Spam", Released: true}); err != nil {
			t.Errorf("MarkPerson(%s) = %v", person, err)
		}
	}
	if got := m.seen(); !slices.Equal(got, []domain.RoomID{"matrix:@ann:x"}) {
		t.Errorf("Matrix was asked about %v, want its own person", got)
	}
	if got := wa.seen(); !slices.Equal(got, []domain.RoomID{whatsappPerson}) {
		t.Errorf("WhatsApp was asked about %v, want its own person", got)
	}
	if err := r.MarkPerson(ctx, domain.PersonVerdict{Person: "slack:T1/U1", Tag: "Spam"}); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("a person on no network = %v, want ErrNetworkOff", err)
	}
}

// A network keeping its own moves is asked for them; every other network's go to the
// store, not refused.
func TestMovesGoToTheNetworkOrTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newFakeMatrix()
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: bare{newFake("telegram")}})
	store := &verdictStore{}
	r.UseVerdicts(store)
	const telegramRoom domain.RoomID = "telegram:42/-1001"
	for _, err := range []error{
		r.MarkTag(ctx, domain.TagVerdict{Room: matrixRoomID, Tag: "Spam"}),
		r.MarkTag(ctx, domain.TagVerdict{Room: telegramRoom, Tag: "Spam"}),
		r.MarkPerson(ctx, domain.PersonVerdict{Person: "matrix:@ann:x", Tag: "Spam"}),
		r.MarkPerson(ctx, domain.PersonVerdict{Person: "telegram:7", Tag: "Spam"}),
	} {
		if err != nil {
			t.Errorf("mark = %v", err)
		}
	}
	if got := m.seen(); !slices.Equal(got, []domain.RoomID{matrixRoomID, "matrix:@ann:x"}) {
		t.Errorf("Matrix kept %v, want its own room and person", got)
	}
	if !slices.Equal(store.kept, []string{string(telegramRoom), "telegram:7"}) {
		t.Errorf("the store kept %v, want Telegram's room and person", store.kept)
	}
	if err := r.MarkTag(ctx, domain.TagVerdict{Room: "slack:T1/C1", Tag: "Spam"}); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("a move on no network = %v, want ErrNetworkOff", err)
	}
}

// A renamed tag is renamed by each network keeping its own, and in the store for the
// rest: never the store over Matrix's mirror, which the homeserver's copy would undo,
// even while Matrix is logged out.
func TestARetagLeavesEachNetworksOwnToIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, loggedOut := range []bool{false, true} {
		m := newFakeMatrix()
		m.loggedOut.Store(loggedOut)
		r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: bare{newFake("telegram")}})
		store := &verdictStore{}
		r.UseVerdicts(store)
		if err := r.RetagVerdicts(ctx, "Spam", "Junk"); err != nil {
			t.Fatalf("RetagVerdicts = %v", err)
		}
		if want := map[bool]int{false: 1, true: 0}[loggedOut]; m.retags != want {
			t.Errorf("logged out %v: Matrix retagged %d times, want %d", loggedOut, m.retags, want)
		}
		if len(store.except) != 1 || !slices.Equal(store.except[0], []domain.Protocol{domain.ProtocolMatrix}) {
			t.Errorf("logged out %v: the store retagged except %v, want once, leaving Matrix", loggedOut, store.except)
		}
	}
}

// A recount reaches each room's own network, and skips networks that are off.
func TestARecountReachesEachRoomsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const telegramRoomID domain.RoomID = "telegram:1001/2002"
	r, m, wa := twoNetworks(t)
	r.RecountUnread(ctx, []domain.RoomID{matrixRoomID, whatsappRoomID, telegramRoomID})
	if got := m.seen(); !slices.Equal(got, []domain.RoomID{matrixRoomID}) {
		t.Errorf("Matrix recounted %v, want its own room", got)
	}
	if got := wa.seen(); !slices.Equal(got, []domain.RoomID{whatsappRoomID}) {
		t.Errorf("WhatsApp recounted %v, want its own room", got)
	}

	r, m, _ = twoNetworks(t)
	m.loggedOut.Store(true)
	r.RecountUnread(ctx, []domain.RoomID{matrixRoomID})
	if got := m.seen(); len(got) != 0 {
		t.Errorf("a logged-out Matrix recounted %v", got)
	}
}

// Every room gets an encryption answer from its own network only; a room nobody can
// answer for reads as encrypted.
func TestEncryptionIsAskedOfEachRoomsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 2))
		m, wa := newFakeMatrix(), newFake("whatsapp")
		others := map[domain.Protocol]Adapter{}
		connected := rng.IntN(2) == 0
		if connected {
			others[domain.ProtocolWhatsApp] = wa
		}
		if rng.IntN(2) == 0 {
			others[domain.ProtocolTelegram] = bare{newFake("telegram")}
		}
		r := routed(m, others)
		rooms := randomRooms(rng)
		got, err := r.RoomEncryption(ctx, rooms)
		where := fmt.Sprintf("seed %d (whatsapp connected %v, rooms %v)", seed, connected, rooms)
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		for _, room := range rooms {
			encrypted, answered := got[room]
			if !answered {
				t.Fatalf("%s: no answer for %s", where, room)
			}
			if domain.NetworkOf(string(room)) == domain.ProtocolWhatsApp && !connected && !encrypted {
				t.Fatalf("%s: %s, on no connected network, reads as not encrypted", where, room)
			}
			if domain.NetworkOf(string(room)) == domain.ProtocolTelegram && !encrypted {
				t.Fatalf("%s: %s, on a network that does not say, reads as not encrypted", where, room)
			}
		}
		for _, room := range m.asked {
			if domain.NetworkOf(string(room)) != domain.ProtocolMatrix {
				t.Fatalf("%s: Matrix was asked about %s", where, room)
			}
		}
		for _, room := range wa.asked {
			if domain.NetworkOf(string(room)) != domain.ProtocolWhatsApp {
				t.Fatalf("%s: WhatsApp was asked about %s", where, room)
			}
		}
	}
}

// withoutMatrix is each way a daemon lacks a usable Matrix: unconfigured beside
// WhatsApp, logged out beside WhatsApp, or logged out alone. m is nil when Matrix is
// not configured, wa when absent.
func withoutMatrix(t *testing.T) map[string]func() (*Router, *fakeMatrix, *fake) {
	t.Helper()
	build := func(configured, whatsapp bool) func() (*Router, *fakeMatrix, *fake) {
		return func() (*Router, *fakeMatrix, *fake) {
			var m *fakeMatrix
			var matrix *fakeMatrix
			if configured {
				m = newFakeMatrix()
				m.loggedOut.Store(true)
				// Anything reaching it fails loudly.
				m.fail = errFake
				m.rooms = []domain.Room{{ID: matrixRoomID}}
				matrix = m
			}
			others := map[domain.Protocol]Adapter{}
			var wa *fake
			if whatsapp {
				wa = newFake("whatsapp")
				wa.rooms = []domain.Room{{ID: whatsappRoomID}}
				others[domain.ProtocolWhatsApp] = wa
			}
			r := routed(matrix, others)
			return r, m, wa
		}
	}
	return map[string]func() (*Router, *fakeMatrix, *fake){
		"not configured, WhatsApp": build(false, true),
		"logged out, WhatsApp":     build(true, true),
		"logged out, no other":     build(true, false),
	}
}

// Without a usable Matrix nothing reaches it: its rooms and actions are refused as
// off, lists are the others' alone, and nobody is anyone on it.
func TestWithoutMatrixNothingReachesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, build := range withoutMatrix(t) {
		r, m, wa := build()
		for call, err := range perRoomCalls(ctx, r, matrixRoomID) {
			if !errors.Is(err, api.ErrNetworkOff) {
				t.Errorf("%s: %s on a Matrix room = %v, want ErrNetworkOff", name, call, err)
			}
		}
		if wa != nil {
			for call, err := range perRoomCalls(ctx, r, whatsappRoomID) {
				if err != nil {
					t.Errorf("%s: %s on a WhatsApp room = %v", name, call, err)
				}
			}
		}

		rooms, err := r.Rooms(ctx)
		if err != nil || slices.ContainsFunc(rooms, func(room domain.Room) bool { return room.ID == matrixRoomID }) {
			t.Errorf("%s: Rooms = (%v, %v), want WhatsApp's alone", name, rooms, err)
		}
		if _, err := r.RefreshRooms(ctx); err != nil {
			t.Errorf("%s: RefreshRooms = %v", name, err)
		}
		if _, err := r.CachedUnread(ctx); err != nil {
			t.Errorf("%s: CachedUnread = %v", name, err)
		}
		if _, err := r.DirectCandidates(ctx, 3); err != nil {
			t.Errorf("%s: DirectCandidates = %v", name, err)
		}
		if got, err := r.MarkRoomsRead(ctx, []domain.RoomID{matrixRoomID}, false); err != nil || got.Failed != 1 {
			t.Errorf("%s: MarkRoomsRead(a Matrix room) = (%+v, %v), want it counted failed", name, got, err)
		}
		if got, err := r.RoomEncryption(ctx, []domain.RoomID{matrixRoomID}); err != nil || !got[matrixRoomID] {
			t.Errorf("%s: RoomEncryption(a Matrix room) = (%v, %v), want encrypted (the safe answer)", name, got, err)
		}

		spaces, serr := r.Spaces(ctx)
		refreshed, rerr := r.RefreshSpaces(ctx)
		invites, ierr := r.CachedInvites(ctx)
		if serr != nil || rerr != nil || ierr != nil || len(spaces)+len(refreshed)+len(invites) != 0 {
			t.Errorf("%s: Matrix's lists = (%v %v %v), (%v %v %v); want empty, no error", name, spaces, refreshed, invites, serr, rerr, ierr)
		}
		actions := map[string]error{}
		_, actions["JoinRoom"] = r.JoinRoom(ctx, "#a:x", nil)
		_, actions["CreateRoom"] = r.CreateRoom(ctx, domain.NewRoom{})
		actions["AddToSpace"] = r.AddToSpace(ctx, mx+"!s:x", matrixRoomID)
		actions["RemoveFromSpace"] = r.RemoveFromSpace(ctx, mx+"!s:x", matrixRoomID)
		actions["InviteUser"] = r.InviteUser(ctx, matrixRoomID, "matrix:@u:x")
		actions["KickUser"] = r.KickUser(ctx, matrixRoomID, "matrix:@u:x", "")
		actions["BanUser"] = r.BanUser(ctx, matrixRoomID, "matrix:@u:x", "")
		actions["UnbanUser"] = r.UnbanUser(ctx, matrixRoomID, "matrix:@u:x")
		actions["LeaveRoom"] = r.LeaveRoom(ctx, matrixRoomID)
		_, actions["ListThreads"] = r.ListThreads(ctx, matrixRoomID)
		_, actions["ThreadPage"] = r.ThreadPage(ctx, matrixRoomID, mx+"$r", "", 10)
		actions["MarkThreadRead"] = r.MarkThreadRead(ctx, matrixRoomID, mx+"$r", mx+"$e", false)
		_, actions["StartVerification"] = r.StartVerification(ctx)
		actions["AcceptVerification"] = r.AcceptVerification(ctx, "t")
		actions["ConfirmSAS"] = r.ConfirmSAS(ctx, "t")
		actions["CancelVerification"] = r.CancelVerification(ctx, "t")
		_, actions["RestoreKeyBackup"] = r.RestoreKeyBackup(ctx, "s")
		_, actions["ExportRoomKeys"] = r.ExportRoomKeys(ctx, "p")
		_, _, actions["ImportRoomKeys"] = r.ImportRoomKeys(ctx, "p", nil)
		_, actions["BootstrapKeyBackup"] = r.BootstrapKeyBackup(ctx, "p")
		for call, err := range actions {
			if !errors.Is(err, api.ErrNetworkOff) {
				t.Errorf("%s: %s = %v, want ErrNetworkOff", name, call, err)
			}
		}
		if r.ThreadParticipant(ctx, matrixRoomID, mx+"$r") {
			t.Errorf("%s: ThreadParticipant is true without Matrix", name)
		}

		if got := r.Me(); slices.Contains(got, "matrix:@matrix:x") {
			t.Errorf("%s: Me = %v, names the logged-out Matrix account", name, got)
		}
		if m != nil {
			if seen, calls := m.seen(), m.calls(); len(seen)+len(calls) != 0 {
				t.Errorf("%s: Matrix was reached: rooms %v, calls %v", name, seen, calls)
			}
		}
		r.Stop()
	}
}

// A Matrix that logs in later is served from then on, through the same router.
func TestMatrixLoggingInIsServedAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, m, _ := withoutMatrix(t)["logged out, WhatsApp"]()
	m.fail = nil
	m.loggedOut.Store(false)
	for call, err := range perRoomCalls(ctx, r, matrixRoomID) {
		if err != nil {
			t.Errorf("%s on a Matrix room after login = %v", call, err)
		}
	}
	if _, err := r.StartVerification(ctx); err != nil {
		t.Errorf("StartVerification after login = %v", err)
	}
	if rooms, err := r.Rooms(ctx); err != nil || len(rooms) != 2 {
		t.Errorf("Rooms after login = (%v, %v), want both networks'", rooms, err)
	}
	if got := r.Me(); !slices.Contains(got, m.account) {
		t.Errorf("Me after login = %v, want Matrix's account in it", got)
	}
}

// Without Matrix its streams carry nothing and end with the router, so pumps return.
func TestWithoutMatrixItsStreamsEndWithTheRouter(t *testing.T) {
	t.Parallel()
	r, _, _ := withoutMatrix(t)["not configured, WhatsApp"]()
	invites, verifications := r.Invites(), r.Verifications()
	r.Stop()
	if _, open := <-invites; open {
		t.Error("Invites sent something")
	}
	if _, open := <-verifications; open {
		t.Error("Verifications sent something")
	}
}

// Spaces join Matrix's hierarchy and WhatsApp's communities by name; one failure fails
// the read; a logged-out Matrix adds none; Matrix cannot file into a community.
func TestSpacesAreEveryNetworks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, wa := newFakeMatrix(), spacedFake{newFake("whatsapp")}
	m.spaces = []domain.Space{{ID: mx + "!work:x", Name: "Work"}}
	community := domain.SpaceID("whatsapp:44881234567/120363@g.us")
	wa.spaces = []domain.Space{{ID: community, Name: "Building", Bridge: domain.ProtocolWhatsApp}}
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolWhatsApp: wa})
	for name, read := range map[string]func(context.Context) ([]domain.Space, error){"Spaces": r.Spaces, "RefreshSpaces": r.RefreshSpaces} {
		got, err := read(ctx)
		if err != nil || len(got) != 2 || got[0].Name != "Building" || got[1].Name != "Work" {
			t.Errorf("%s = (%v, %v), want both networks', by name", name, got, err)
		}
	}

	wa.fail = errFake
	if got, err := r.Spaces(ctx); !errors.Is(err, errFake) || got != nil {
		t.Errorf("Spaces with WhatsApp failing = (%v, %v), want the failure and no partial list", got, err)
	}
	wa.fail = nil

	for call, err := range map[string]error{
		"AddToSpace":      r.AddToSpace(ctx, community, matrixRoomID),
		"RemoveFromSpace": r.RemoveFromSpace(ctx, community, whatsappRoomID),
	} {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s on a WhatsApp community = %v, want ErrNotOnNetwork", call, err)
		}
	}
	if got := m.calls(); slices.Contains(got, "AddToSpace") || slices.Contains(got, "RemoveFromSpace") {
		t.Errorf("Matrix was asked to file into a WhatsApp community: %v", got)
	}

	m.loggedOut.Store(true)
	if got, err := r.Spaces(ctx); err != nil || len(got) != 1 || got[0].ID != community {
		t.Errorf("Spaces with Matrix logged out = (%v, %v), want WhatsApp's alone", got, err)
	}
}

// A network with its own threads is asked about its rooms' threads, never Matrix; one
// without threads refuses them.
func TestThreadsGoToTheRoomsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const slackRoomID domain.RoomID = "slack:T1/C1"
	m, sl, wa := newFakeMatrix(), threadedFake{newFake("slack")}, newFake("whatsapp")
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolSlack: sl, domain.ProtocolWhatsApp: wa})
	threads, err := r.ListThreads(ctx, slackRoomID)
	if err != nil || len(threads) != 1 || threads[0].RoomID != slackRoomID {
		t.Errorf("ListThreads on a Slack room = (%v, %v), want Slack's", threads, err)
	}
	if _, err := r.ThreadPage(ctx, slackRoomID, "r", "", 10); err != nil {
		t.Errorf("ThreadPage on a Slack room: %v", err)
	}
	if err := r.MarkThreadRead(ctx, slackRoomID, "r", "e", false); err != nil {
		t.Errorf("MarkThreadRead on a Slack room: %v", err)
	}
	if !r.ThreadParticipant(ctx, slackRoomID, "r") {
		t.Error("ThreadParticipant on a Slack room did not reach Slack")
	}
	if seen := sl.seen(); len(seen) != 4 {
		t.Errorf("Slack heard of %v, want all 4 calls", seen)
	}
	if got := m.calls(); len(got) != 0 {
		t.Errorf("Matrix heard of a Slack room's threads: %v", got)
	}

	sl.fail = errFake
	if _, err := r.ListThreads(ctx, slackRoomID); !errors.Is(err, errFake) {
		t.Errorf("ListThreads with Slack failing = %v, want its failure", err)
	}
	if r.ThreadParticipant(ctx, slackRoomID, "r") {
		t.Error("ThreadParticipant is true with Slack failing")
	}
	if _, err := r.ListThreads(ctx, whatsappRoomID); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("ListThreads on a WhatsApp room = %v, want ErrNotOnNetwork", err)
	}
}

// A network lacking a capability refuses what needs it and is left out of lists; its
// rooms name no parent and have no thread participation.
func TestANetworkWithoutACapabilityRefusesWhatNeedsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newFakeMatrix()
	m.rooms = []domain.Room{{ID: matrixRoomID}}
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: bare{newFake("telegram")}})
	room := domain.RoomID("telegram:42/-1001")
	for name, err := range map[string]error{
		"send":    r.Send(ctx, room, domain.Draft{Body: "hi"}),
		"mark":    r.MarkRead(ctx, room, "", false),
		"star":    r.StarMessage(ctx, room, "telegram:42/-1001/7", true),
		"members": func() error { _, err := r.Members(ctx, room, 5); return err }(),
		"threads": func() error { _, err := r.ListThreads(ctx, room); return err }(),
		"invite":  r.InviteUser(ctx, room, "telegram:7"),
		"person":  r.MarkPerson(ctx, domain.PersonVerdict{Person: "telegram:7", Tag: "Spam", Released: true}),
	} {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s in a Telegram room = %v, want not on that network", name, err)
		}
	}
	if parent, err := r.CanonicalParent(ctx, room); parent != "" || err != nil {
		t.Errorf("CanonicalParent = (%q, %v), want none", parent, err)
	}
	if r.ThreadParticipant(ctx, room, "telegram:42/-1001/7") {
		t.Error("took part in a thread of a network without threads")
	}
	if rooms, err := r.Rooms(ctx); err != nil || len(rooms) != 1 || rooms[0].ID != matrixRoomID {
		t.Errorf("Rooms = (%v, %v), want Matrix's alone", rooms, err)
	}
}

// chatMaker is a network that makes chats, recording what it was asked to make.
type chatMaker struct {
	*fake
	made []domain.NewRoom
}

func (c *chatMaker) CreateRoom(_ context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	c.made = append(c.made, spec)
	return "telegram:42/-1009", nil
}

func (c *chatMaker) ChatOffers(context.Context) ([]domain.ChatOffer, error) {
	return []domain.ChatOffer{{On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Kind: domain.ChatForum, Label: "Telegram forum"}}, nil
}

// A chat for an account goes to that account's network; a network that makes none
// refuses; no account named means Matrix.
func TestANewChatGoesToItsAccountsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tg := newFakeMatrix(), &chatMaker{fake: newFake("telegram")}
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: tg, domain.ProtocolWhatsApp: bare{newFake("whatsapp")}})
	spec := domain.NewRoom{Name: "Hikers", On: domain.AccountRooms(domain.ProtocolTelegram, "42"), Kind: domain.ChatForum}
	if id, err := r.CreateRoom(ctx, spec); err != nil || id != "telegram:42/-1009" || len(tg.made) != 1 || tg.made[0].Kind != domain.ChatForum {
		t.Errorf("CreateRoom on Telegram = (%q, %v), made %+v", id, err, tg.made)
	}
	if got := m.calls(); len(got) != 0 {
		t.Errorf("Matrix heard of a Telegram chat: %v", got)
	}
	spec.On = domain.AccountRooms(domain.ProtocolWhatsApp, "15550100001")
	if _, err := r.CreateRoom(ctx, spec); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("CreateRoom on a network that makes none = %v, want not on that network", err)
	}
	if _, err := r.CreateRoom(ctx, domain.NewRoom{Name: "Plain"}); err != nil || !slices.Contains(m.calls(), "CreateRoom") {
		t.Errorf("a room with no account = %v, Matrix calls %v", err, m.calls())
	}
	// What chats can be made is asked of the networks that make them, and only them.
	if offers, err := r.ChatOffers(ctx); err != nil || len(offers) != 1 || offers[0].Kind != domain.ChatForum {
		t.Errorf("ChatOffers = (%+v, %v), want Telegram's forum alone", offers, err)
	}
}

// linkFollower is a network that follows its links, recording what it was asked.
type linkFollower struct {
	*fake
	resolved, joined []string
}

func (l *linkFollower) ResolveLink(_ context.Context, uri string, on domain.RoomOwner) (domain.LinkTarget, error) {
	l.resolved = append(l.resolved, uri+" on "+string(on))
	return domain.LinkTarget{Room: domain.Room{ID: "telegram:42/7", IsDirect: true}}, nil
}

func (l *linkFollower) JoinLink(_ context.Context, uri string, on domain.RoomOwner) (domain.RoomID, error) {
	l.joined = append(l.joined, uri+" on "+string(on))
	return "telegram:42/-1009", nil
}

// A link goes to the account named, on the network it is a link for; a link no network
// reads, one for a network that does not follow links, or one named an account of
// another network, is refused.
func TestALinkGoesToItsNetwork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tg := newFakeMatrix(), &linkFollower{fake: newFake("telegram")}
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: tg})
	on := domain.AccountRooms(domain.ProtocolTelegram, "42")
	if target, err := r.ResolveLink(ctx, "tg://resolve?domain=novosalsa", on); err != nil || target.Room.ID != "telegram:42/7" {
		t.Errorf("ResolveLink = (%+v, %v), want Telegram's answer", target, err)
	}
	if id, err := r.JoinLink(ctx, "https://t.me/+AbCdEf", on); err != nil || id != "telegram:42/-1009" {
		t.Errorf("JoinLink = (%q, %v), want Telegram's chat", id, err)
	}
	if len(tg.resolved) != 1 || len(tg.joined) != 1 || !strings.HasSuffix(tg.resolved[0], " on "+string(on)) {
		t.Errorf("Telegram was asked to resolve %v and join %v, want one each", tg.resolved, tg.joined)
	}
	if _, err := r.ResolveLink(ctx, "https://example.org/x", on); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("a link for no network = %v, want not on a network", err)
	}
	if _, err := r.ResolveLink(ctx, "tg://resolve?domain=novosalsa", domain.AccountRooms(domain.ProtocolMatrix, "0123456789abcdef")); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("a Telegram link on a Matrix account = %v, want it refused", err)
	}
	bareTG := routed(m, map[domain.Protocol]Adapter{domain.ProtocolTelegram: bare{newFake("telegram")}})
	if _, err := bareTG.ResolveLink(ctx, "tg://resolve?domain=novosalsa", on); !errors.Is(err, api.ErrNotOnNetwork) {
		t.Errorf("a network that follows no links = %v, want not on that network", err)
	}
	if got := m.calls(); len(got) != 0 {
		t.Errorf("Matrix heard of a Telegram link: %v", got)
	}
}

// A room as Matrix writes it, an alias or a bare room ID from a link or a typed join,
// goes to Matrix: it names no account yet.
func TestAMatrixAddressJoinsOnMatrix(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"#lobby:x", "!lobby:x"} {
		r, m, wa := twoNetworks(t)
		if _, err := r.JoinRoom(context.Background(), address, nil); err != nil {
			t.Fatalf("JoinRoom(%s) = %v", address, err)
		}
		m.mu.Lock()
		joined := slices.Contains(m.matrixCalls, "JoinRoom")
		m.mu.Unlock()
		if !joined || len(wa.seen()) != 0 {
			t.Errorf("JoinRoom(%s) reached Matrix %t, WhatsApp %v", address, joined, wa.seen())
		}
	}
}

// Accounts is every live network's accounts, each network's own.
func TestAccountsAreEveryNetworksOwn(t *testing.T) {
	t.Parallel()
	r, _, _ := twoNetworks(t)
	got, err := r.Accounts(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("Accounts = %+v, %v; want Matrix's and WhatsApp's", got, err)
	}
	networks := []domain.Protocol{got[0].Network, got[1].Network}
	if !slices.Contains(networks, domain.ProtocolMatrix) || !slices.Contains(networks, domain.ProtocolWhatsApp) {
		t.Errorf("networks = %v", networks)
	}
}
