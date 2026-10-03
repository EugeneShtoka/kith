package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// errTest is the failure the membership handlers are shown.
var errTest = errors.New("no")

// membershipBackend records the join/leave calls the model makes.
type membershipBackend struct {
	apitest.Nop
	joined    []string
	joinedVia []string
	left      []domain.RoomID
	err       error
}

func (b *membershipBackend) JoinRoom(_ context.Context, target string, via []string) (domain.RoomID, error) {
	b.joined = append(b.joined, target)
	b.joinedVia = via
	if b.err != nil {
		return "", b.err
	}
	return domain.RoomID(target), nil
}

func (b *membershipBackend) LeaveRoom(_ context.Context, roomID domain.RoomID) error {
	b.left = append(b.left, roomID)
	return b.err
}

// invited builds a pending invitation.
func invited(id domain.RoomID, name, by string) domain.Room {
	return domain.Room{ID: id, Name: name, Membership: domain.MembershipInvite, InvitedBy: by}
}

// withInvites applies an invitation set and returns the updated model.
func withInvites(t *testing.T, m Model, invites ...domain.Room) Model {
	t.Helper()
	next, _ := m.handleInvites(invites)
	got := next
	return got
}

// onInvite selects the first invitation in the invites group, with the room list
// focused — the state every invitation action is taken from.
func onInvite(t *testing.T, m Model) Model {
	t.Helper()
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, inviteGroupKey)
	fr := m.filteredRooms()
	if len(fr) == 0 {
		t.Fatal("the invites group is empty")
	}
	next, _ := m.selectRoom(fr[0])
	return next
}

// With nothing pending, the rail is exactly what it was before invitations existed —
// the feature costs the user nothing until it has something to say.
func TestNoInvitesLeavesRailAlone(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	for _, g := range m.rail.groups {
		if g.key == inviteGroupKey {
			t.Fatal("an invites group should not exist with no invitations")
		}
	}
	m = withInvites(t, m) // an empty set changes nothing
	for _, g := range m.rail.groups {
		if g.key == inviteGroupKey {
			t.Error("an empty invitation set should not create the group")
		}
	}
}

// An invitation appears in its own group, counted, and nowhere else — it is a decision,
// not a room you can open by accident from the room list.
func TestInvitesGetTheirOwnGroup(t *testing.T) {
	t.Parallel()

	m := withInvites(t, sized(t, withRooms(t, newModel())),
		invited("!i1:x", "Design Review", "@alice:x"),
		invited("!i2:x", "Standup", "@bob:x"))

	if m.rail.groups[0].key != inviteGroupKey {
		t.Fatalf("groups = %v, want invites first", groupKeys(m.rail.groups))
	}
	if row := m.railRow(m.rail.groups[0], false, false, 30); !strings.Contains(row, "Invites (2)") {
		t.Errorf("row = %q, want Invites (2)", row)
	}
	// Every other group excludes them.
	for i, g := range m.rail.groups {
		if i == 0 {
			continue
		}
		for _, room := range m.rooms.all {
			if room.IsInvite() && g.admits(m.unreadView(), room) {
				t.Errorf("group %q should not list the invitation %s", g.key, room.ID)
			}
		}
	}
	// And the invites group lists only them.
	m.rail.cursor = 0
	fr := m.filteredRooms()
	if len(fr) != 2 {
		t.Fatalf("invites group has %d rooms, want 2", len(fr))
	}
	for _, room := range fr {
		if !room.IsInvite() {
			t.Errorf("%s is not an invitation but is in the invites group", room.ID)
		}
	}
	// A new invitation is announced, since a group you aren't looking at is exactly
	// what you miss.
	if !strings.Contains(m.status(), "2 new invitations") {
		t.Errorf("status = %q, want an announcement", m.status())
	}
}

// A refresh of the joined rooms must not drop the invitations, and a revised invitation
// set must not drop the rooms.
func TestRefreshAndInvitesDoNotClobberEachOther(t *testing.T) {
	t.Parallel()

	m := withInvites(t, sized(t, withRooms(t, newModel())), invited("!i1:x", "Invited", "@alice:x"))
	if len(m.rooms.all) != 3 {
		t.Fatalf("rooms = %d, want 2 joined + 1 invitation", len(m.rooms.all))
	}

	// A room-list refresh arrives (joined rooms only).
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
		{ID: "!c:x", Name: "Charlie"},
	}})
	if _, ok := roomByIDIn(m.rooms.all, "!i1:x"); !ok {
		t.Error("a room refresh dropped the invitation")
	}
	if len(m.rooms.all) != 4 {
		t.Errorf("rooms = %d, want 3 joined + 1 invitation", len(m.rooms.all))
	}

	// And a revised invitation set keeps the joined rooms.
	m = withInvites(t, m, invited("!i2:x", "Another", "@bob:x"))
	if _, ok := roomByIDIn(m.rooms.all, "!c:x"); !ok {
		t.Error("an invitation update dropped a joined room")
	}
	if _, ok := roomByIDIn(m.rooms.all, "!i1:x"); ok {
		t.Error("the answered invitation should be gone — the set is authoritative")
	}
}

// Accepting is joining, and needs no confirmation: it is what the invitation is for,
// and leaving undoes it.
func TestAcceptInviteJoins(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	m := withInvites(t, sized(t, update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})),
		invited("!i1:x", "Design Review", "@alice:x"))
	m = onInvite(t, m)

	m, cmd := press(t, m, keyText("y"))
	if cmd == nil {
		t.Fatal("accept produced no command")
	}
	runCmd(t, cmd)
	if len(b.joined) != 1 || b.joined[0] != "!i1:x" {
		t.Errorf("JoinRoom calls = %v, want [!i1:x]", b.joined)
	}
	if m.confirm.active() {
		t.Error("accepting should not ask for confirmation")
	}
}

// Rejecting asks first, and only leaves once the answer is yes.
func TestRejectInviteConfirmsFirst(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	base := withInvites(t, sized(t, update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})),
		invited("!i1:x", "Design Review", "@alice:x"))

	// Declining the question does nothing at all.
	m := onInvite(t, base)
	m, _ = press(t, m, keyText("d"))
	if !m.confirm.active() {
		t.Fatal("reject should ask before acting")
	}
	if !strings.Contains(m.confirmPrompt(), "Design Review") {
		t.Errorf("prompt = %q, should name the room", m.confirmPrompt())
	}
	m, cmd := press(t, m, keyText("n"))
	if untimed(t, cmd) != nil {
		t.Error("answering no should not act")
	}
	if m.confirm.active() {
		t.Error("answering no should close the question")
	}
	if len(b.left) != 0 {
		t.Errorf("LeaveRoom was called after a no: %v", b.left)
	}

	// Confirming leaves the room, which is how Matrix rejects an invitation.
	m = onInvite(t, base)
	m, _ = press(t, m, keyText("d"))
	_, cmd = press(t, m, keyText("y"))
	if cmd == nil {
		t.Fatal("answering yes produced no command")
	}
	runCmd(t, cmd)
	if len(b.left) != 1 || b.left[0] != "!i1:x" {
		t.Errorf("LeaveRoom calls = %v, want [!i1:x]", b.left)
	}
}

// Leaving a joined room asks too, and applies to the joined room — not to an
// invitation, which has its own answer.
func TestLeaveConfirmsAndOnlyAppliesToJoinedRooms(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	m := withInvites(t, sized(t, update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})),
		invited("!i1:x", "Invited", "@alice:x"))

	// On an invitation, leave is inert — reject is the action that fits.
	inv := onInvite(t, m)
	inv, _ = press(t, inv, keyCode('L'))
	if inv.confirm.active() {
		t.Error("leave should be inert on an invitation")
	}
	// And accept is inert on a joined room.
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	joined, _ := m.selectRoom(m.filteredRooms()[0])
	jm := joined
	jm2, cmd := press(t, jm, keyText("y"))
	if cmd != nil || jm2.confirm.active() {
		t.Error("accept should be inert on a joined room")
	}

	// On a joined room, leave asks and then acts.
	jm, _ = press(t, jm, keyCode('L'))
	if !jm.confirm.active() {
		t.Fatal("leave should ask before acting")
	}
	if !strings.Contains(jm.confirmPrompt(), "Alpha") {
		t.Errorf("prompt = %q, should name the room", jm.confirmPrompt())
	}
	_, cmd = press(t, jm, keyText("y"))
	if cmd == nil {
		t.Fatal("answering yes produced no command")
	}
	runCmd(t, cmd)
	if len(b.left) != 1 || b.left[0] != "!a:x" {
		t.Errorf("LeaveRoom calls = %v, want [!a:x]", b.left)
	}
}

// A pending question captures the keyboard: a question you can walk away from gets
// answered by accident later.
func TestConfirmCapturesKeys(t *testing.T) {
	t.Parallel()

	m := onInvite(t, withInvites(t, sized(t, withRooms(t, newModel())),
		invited("!i1:x", "Invited", "@alice:x")))
	m, _ = press(t, m, keyText("d"))
	before := m.rail.cursor

	for _, key := range []tea.KeyPressMsg{keyText("j"), keyText("k"), {Code: tea.KeyTab}, keyText("?")} {
		next, _ := press(t, m, key)
		if !next.confirm.active() {
			t.Errorf("%q should not dismiss the question", key.String())
		}
		if next.rail.cursor != before || next.reader.showing(readerHelp) || next.focus != m.focus {
			t.Errorf("%q leaked past the question", key.String())
		}
	}
	// esc always answers no, whatever the config says.
	next, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.confirm.active() {
		t.Error("esc should close the question")
	}
}

// The join prompt takes a room address, types freely, and joins on submit.
func TestJoinPrompt(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	m := sized(t, update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	m.focus = paneRooms

	m, _ = press(t, m, keyCode('J'))
	if m.prompt.kind != promptJoin {
		t.Fatal("J should open the join prompt")
	}
	// Letters bound to other actions still type — same rule as the composer.
	for _, r := range "#jkl:x" {
		m, _ = press(t, m, keyText(string(r)))
	}
	if m.prompt.input != "#jkl:x" {
		t.Fatalf("joinInput = %q, want #jkl:x", m.prompt.input)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.prompt.input != "#jkl:" {
		t.Errorf("backspace gave %q, want #jkl:", m.prompt.input)
	}
	m2, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting produced no command")
	}
	runCmd(t, cmd)
	if len(b.joined) != 1 || b.joined[0] != "#jkl:" {
		t.Errorf("JoinRoom calls = %v, want [#jkl:]", b.joined)
	}
	if m2.prompt.active() {
		t.Error("submitting should close the prompt")
	}

	// esc abandons it, and an empty submit just closes.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.prompt.active() || m.prompt.input != "" {
		t.Error("esc should close and clear the prompt")
	}
	m, _ = press(t, m, keyCode('J'))
	_, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("an empty prompt should not try to join")
	}
}

// Selecting an invitation must not fetch a history we are not allowed to read — that
// 403 would make a perfectly normal invitation look broken.
func TestSelectingInviteFetchesNothing(t *testing.T) {
	t.Parallel()

	m := onInvite(t, withInvites(t, sized(t, withRooms(t, newModel())),
		invited("!i1:x", "Invited", "@alice:x")))
	if m.timeline.hist.loading {
		t.Error("selecting an invitation should not start a history fetch")
	}
	if m.status() != "" {
		t.Errorf("status = %q, want it left alone", m.status())
	}
	// And opening it goes nowhere: there is no timeline behind it.
	next, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.focus == paneTimeline {
		t.Error("an invitation should not open into the timeline pane")
	}
}

// The decision pane says what is being decided and by whom.
func TestInviteDecisionPane(t *testing.T) {
	t.Parallel()

	m := onInvite(t, withInvites(t, sized(t, withRooms(t, newModel())),
		invited("!i1:x", "Design Review", "@alice:x")))
	body := stripStyles(m.View().Content)
	for _, want := range []string{"invited to", "Design Review", "@alice:x", "accept", "reject"} {
		if !strings.Contains(body, want) {
			t.Errorf("the decision pane should mention %q", want)
		}
	}
}

// A leave drops the room locally at once, so a second keystroke isn't aimed at a room
// that is already gone.
func TestLeftDropsRoomImmediately(t *testing.T) {
	t.Parallel()

	m := withInvites(t, sized(t, withRooms(t, newModel())), invited("!i1:x", "Invited", "@alice:x"))
	m = update(t, m, leftMsg{roomID: "!i1:x"})
	if _, ok := roomByIDIn(m.rooms.all, "!i1:x"); ok {
		t.Error("the left room should be gone from the list")
	}
	for _, g := range m.rail.groups {
		if g.key == inviteGroupKey {
			t.Error("the invites group should disappear with its last invitation")
		}
	}

	m2 := update(t, sized(t, withRooms(t, newModel())), leftMsg{roomID: "!a:x"})
	if _, ok := roomByIDIn(m2.rooms.joined, "!a:x"); ok {
		t.Error("the left joined room should be gone")
	}
}

// A failed membership change says so rather than pretending it worked.
func TestMembershipFailuresSurface(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, joinedMsg{err: errTest})
	if !strings.Contains(m.status(), "join failed") {
		t.Errorf("status = %q, want a join failure", m.status())
	}
	m = update(t, m, leftMsg{roomID: "!a:x", err: errTest})
	if !strings.Contains(m.status(), "leave failed") {
		t.Errorf("status = %q, want a leave failure", m.status())
	}
	// A room that failed to leave stays in the list.
	if _, ok := roomByIDIn(m.rooms.all, "!a:x"); !ok {
		t.Error("a failed leave should not remove the room")
	}
}

// A config that placed the invites group deliberately is respected; one written before
// invitations existed still gets them at the top rather than buried.
func TestInvitesGroupPlacement(t *testing.T) {
	t.Parallel()

	spaces := []domain.Space{{ID: "!w:x", Name: "Work"}}
	view := starterView(t, unreadView{})
	rooms := []domain.Room{invited("!i:x", "Invited", "@alice:x")}

	// The user never mentioned invites: it is promoted to the front even though
	// applyRailConfig demotes unlisted groups.
	unaware := railGroups(spaces, config.Rail{
		Order:  []string{unreadGroupKey, "Work"},
		Hidden: []string{homeGroupKey, dmsGroupKey},
	}, nil, view, rooms)
	if got := groupKeys(unaware); got[0] != inviteGroupKey {
		t.Errorf("groups = %v, want invites first", got)
	}

	// The user placed it: left where they put it.
	aware := railGroups(spaces, config.Rail{
		Order:  []string{unreadGroupKey, inviteGroupKey, "Work"},
		Hidden: []string{homeGroupKey, dmsGroupKey},
	}, nil, view, rooms)
	if got := groupKeys(aware); got[1] != inviteGroupKey {
		t.Errorf("groups = %v, want invites where the config put it", got)
	}

	// Hiding it works like any other group.
	hidden := railGroups(spaces, config.Rail{Hidden: []string{inviteGroupKey}}, nil, view, rooms)
	for _, k := range groupKeys(hidden) {
		if k == inviteGroupKey {
			t.Error("a hidden invites group should not appear")
		}
	}
}

func TestNewInvitesCounting(t *testing.T) {
	t.Parallel()

	a := invited("!1:x", "A", "@x:y")
	b := invited("!2:x", "B", "@x:y")
	tests := []struct {
		name       string
		prev, next []domain.Room
		want       int
	}{
		{"first arrival", nil, []domain.Room{a}, 1},
		{"two at once", nil, []domain.Room{a, b}, 2},
		{"unchanged", []domain.Room{a}, []domain.Room{a}, 0},
		{"one answered", []domain.Room{a, b}, []domain.Room{a}, 0},
		{"one answered, one new", []domain.Room{a}, []domain.Room{b}, 1},
		{"all answered", []domain.Room{a, b}, nil, 0},
	}
	for _, tc := range tests {
		if got := newInvites(tc.prev, tc.next); got != tc.want {
			t.Errorf("%s: newInvites = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// groupKeys lists a rail's group keys.
func groupKeys(groups []group) []string {
	out := make([]string, len(groups))
	for i := range groups {
		out[i] = groups[i].key
	}
	return out
}

// roomByIDIn finds a room in a slice.
func roomByIDIn(rooms []domain.Room, id domain.RoomID) (domain.Room, bool) {
	if at := indexOfRoom(rooms, id); at >= 0 {
		return rooms[at], true
	}
	return domain.Room{}, false
}

// stripStyles removes ANSI escapes so a test can match rendered text.
func stripStyles(s string) string { return ansi.Strip(s) }
