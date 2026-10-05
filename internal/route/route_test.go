package route

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Two networks' rooms, as their IDs name them.
const (
	matrixRoomID   domain.RoomID = "!a:x"
	whatsappRoomID domain.RoomID = "whatsapp:44881234567/120363@g.us"
)

// twoNetworks routes a Matrix fake and a WhatsApp fake.
func twoNetworks(t *testing.T) (*Router, *fakeMatrix, *fake) {
	t.Helper()
	m, wa := newFakeMatrix(), newFake("whatsapp")
	r := routed(m, map[domain.Protocol]Adapter{domain.ProtocolWhatsApp: wa})
	return r, m, wa
}

// perRoomCalls is every call about one room, made through the router.
func perRoomCalls(ctx context.Context, r *Router, room domain.RoomID) map[string]error {
	calls := map[string]error{}
	calls["MarkRead"] = r.MarkRead(ctx, room, "$e", false)
	calls["MarkRoomUnread"] = r.MarkRoomUnread(ctx, room, true)
	calls["StarMessage"] = r.StarMessage(ctx, room, "$e", true)
	calls["MarkSpam"] = r.MarkSpam(ctx, domain.SpamVerdict{Room: room})
	_, calls["CanonicalParent"] = r.CanonicalParent(ctx, room)
	_, _, calls["MessageHistory"] = r.MessageHistory(ctx, room, "$e")
	_, calls["Timeline"] = r.Timeline(ctx, room, "", 10)
	_, calls["FetchEvent"] = r.FetchEvent(ctx, room, "$e")
	calls["Redact"] = r.Redact(ctx, room, "$e", "")
	calls["Send"] = r.Send(ctx, room, domain.Draft{Body: "hi"})
	calls["SendTyping"] = r.SendTyping(ctx, room, true, time.Second)
	calls["SendFile"] = r.SendFile(ctx, room, "/f", "")
	calls["SendReaction"] = r.SendReaction(ctx, room, "$e", "👍")
	_, calls["LoadImage"] = r.LoadImage(ctx, room, "$e")
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

// What only Matrix has is refused for another network's room before Matrix hears of
// it, and reaches Matrix for its own.
func TestMatrixOnlyCallsStayOnMatrix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	calls := func(r *Router, room domain.RoomID) map[string]error {
		out := map[string]error{}
		out["AddToSpace"] = r.AddToSpace(ctx, "!s:x", room)
		out["RemoveFromSpace"] = r.RemoveFromSpace(ctx, "!s:x", room)
		out["InviteUser"] = r.InviteUser(ctx, room, "@u:x")
		out["KickUser"] = r.KickUser(ctx, room, "@u:x", "")
		out["BanUser"] = r.BanUser(ctx, room, "@u:x", "")
		out["UnbanUser"] = r.UnbanUser(ctx, room, "@u:x")
		out["LeaveRoom"] = r.LeaveRoom(ctx, room)
		_, out["ListThreads"] = r.ListThreads(ctx, room)
		_, out["ThreadPage"] = r.ThreadPage(ctx, room, "$r", "", 10)
		out["MarkThreadRead"] = r.MarkThreadRead(ctx, room, "$r", "$e", false)
		return out
	}

	r, m, wa := twoNetworks(t)
	for name, err := range calls(r, whatsappRoomID) {
		if !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s on a WhatsApp room = %v, want ErrNotOnNetwork", name, err)
		}
	}
	if r.ThreadParticipant(ctx, whatsappRoomID, "$r") {
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
	if !r.ThreadParticipant(ctx, matrixRoomID, "$r") {
		t.Error("ThreadParticipant on a Matrix room did not reach Matrix")
	}
	if got := m.calls(); len(got) != len(mine)+1 {
		t.Errorf("Matrix heard %v, want all %d calls", got, len(mine)+1)
	}
}

// Lists are every network's, Matrix first; one network failing fails the read rather
// than passing a partial list as whole.
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

// With Matrix alone, the router changes nothing: its streams are Matrix's own.
func TestOneNetworkIsPassedThrough(t *testing.T) {
	t.Parallel()
	m := newFakeMatrix()
	r := routed(m, nil)
	if r.Messages() != (<-chan domain.Message)(m.messages) || r.Unread() != (<-chan domain.Unread)(m.unread) ||
		r.Reactions() != (<-chan domain.ReactionUpdate)(m.reactions) || r.Activity() != (<-chan domain.Activity)(m.activity) {
		t.Error("a lone network's streams were wrapped")
	}
}

// Me is everyone this person is on every network, once each; the account is
// Matrix's, the one the daemon is named after.
func TestMeIsEveryNetworksSelf(t *testing.T) {
	t.Parallel()
	r, m, wa := twoNetworks(t)
	m.me = []string{"@me:x", "@whatsapp_me:x"}
	wa.me = []string{"whatsapp:44881234567@s.whatsapp.net", "@me:x"}
	if got := r.Me(); !slices.Equal(got, []string{"@me:x", "@whatsapp_me:x", "whatsapp:44881234567@s.whatsapp.net"}) {
		t.Errorf("Me = %v", got)
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

// Every network is optional, Matrix too: with none, the daemon runs empty until :login sets one up — Start
// waits for the end, and lists are empty.
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

// randomRooms is a set of rooms spread over Matrix, WhatsApp (when connected), Telegram
// (a bare adapter, when there) and a network with no adapter (a second WhatsApp
// account's rooms count as WhatsApp's).
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

// Marking rooms read, over any mix of networks and refusals: every room is counted
// exactly once, each reaches only its own network, and an error means nothing was
// attempted anywhere.
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

// Encryption over any mix of networks: every room gets an answer, from its own
// network only, and a room no network can answer for — none connected, or one that
// does not say — reads as encrypted.
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

// withoutMatrix is each way a daemon can be without a usable Matrix: not configured
// beside WhatsApp, configured but not logged in beside WhatsApp, or configured, not
// logged in and alone. m is nil when Matrix is not configured; wa when absent.
func withoutMatrix(t *testing.T) map[string]func() (*Router, *fakeMatrix, *fake) {
	t.Helper()
	build := func(configured, whatsapp bool) func() (*Router, *fakeMatrix, *fake) {
		return func() (*Router, *fakeMatrix, *fake) {
			var m *fakeMatrix
			var matrix *fakeMatrix
			if configured {
				m = newFakeMatrix()
				m.loggedOut.Store(true)
				// Anything that reached it would fail, and say so.
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

// Without a usable Matrix nothing reaches it: its rooms are refused as off, lists are
// the other networks' alone, its own lists are empty, its own actions are refused as
// off, and nobody is anyone on it. WhatsApp is served as before.
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
		actions["AddToSpace"] = r.AddToSpace(ctx, "!s:x", matrixRoomID)
		actions["RemoveFromSpace"] = r.RemoveFromSpace(ctx, "!s:x", matrixRoomID)
		actions["InviteUser"] = r.InviteUser(ctx, matrixRoomID, "@u:x")
		actions["KickUser"] = r.KickUser(ctx, matrixRoomID, "@u:x", "")
		actions["BanUser"] = r.BanUser(ctx, matrixRoomID, "@u:x", "")
		actions["UnbanUser"] = r.UnbanUser(ctx, matrixRoomID, "@u:x")
		actions["LeaveRoom"] = r.LeaveRoom(ctx, matrixRoomID)
		_, actions["ListThreads"] = r.ListThreads(ctx, matrixRoomID)
		_, actions["ThreadPage"] = r.ThreadPage(ctx, matrixRoomID, "$r", "", 10)
		actions["MarkThreadRead"] = r.MarkThreadRead(ctx, matrixRoomID, "$r", "$e", false)
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
		if r.ThreadParticipant(ctx, matrixRoomID, "$r") {
			t.Errorf("%s: ThreadParticipant is true without Matrix", name)
		}

		if got := r.Me(); slices.Contains(got, "@matrix:x") {
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

// Without Matrix configured its streams carry nothing and end when the router stops,
// so the daemon's pumps over them return.
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

// Spaces are every network's: Matrix's hierarchy and WhatsApp's communities, by name;
// a network failing fails the read; a logged-out Matrix adds none. A WhatsApp
// community is not Matrix's to file rooms into.
func TestSpacesAreEveryNetworks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, wa := newFakeMatrix(), spacedFake{newFake("whatsapp")}
	m.spaces = []domain.Space{{ID: "!work:x", Name: "Work"}}
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

// A network with threads of its own is asked about its rooms' threads, and Matrix
// never is; a network without threads still refuses them.
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

// A network lacking a capability refuses what needs it, as not on that network, and
// is left out of what it cannot list; a room it serves names no parent, and its
// threads were never taken part in.
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
