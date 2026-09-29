package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// bulkReader records the rooms MarkRoomsRead was asked about and returns a fixed result.
type bulkReader struct {
	apitest.Nop
	calls  int
	asked  []domain.RoomID
	result domain.ReadResult
	err    error
}

func (b *bulkReader) MarkRoomsRead(_ context.Context, roomIDs []domain.RoomID, _ bool) (domain.ReadResult, error) {
	b.calls++
	b.asked = append([]domain.RoomID(nil), roomIDs...)
	if b.err != nil {
		return domain.ReadResult{}, b.err
	}
	return b.result, b.err
}

// reading returns a model with three rooms (two unread, one in Work), rail focused.
func reading(t *testing.T) (Model, *bulkReader) {
	t.Helper()
	b := &bulkReader{result: domain.ReadResult{Marked: 1}}
	m := update(t, New(context.Background(), b, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
		{ID: "!c:x", Name: "Cyan"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{
		{RoomID: "!a:x", Notifications: 3},
		{RoomID: "!c:x", Notifications: 1},
	}})
	m = m.clearStatus()
	return sized(t, m), b
}

// The room list's mark-read names one room and asks nothing.
func TestMarkRoomReadFromTheRoomList(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next

	m, cmd := press(t, m, keyText("m"))
	if m.confirm.active() {
		t.Error("marking one room read should not ask")
	}
	if cmd == nil {
		t.Fatal("m in the room list sent no command")
	}
	msg, ok := msgOf[markedReadMsg](t, cmd)
	if !ok {
		t.Fatal("the command produced no markedReadMsg")
	}
	if want := []domain.RoomID{"!a:x"}; !reflect.DeepEqual(b.asked, want) {
		t.Errorf("backend asked for %v, want %v", b.asked, want)
	}
	m = update(t, m, msg)
	if m.status() != "marked Alpha read" {
		t.Errorf("status = %q, want it to name the room", m.status())
	}
}

// An invitation is answered, not read.
func TestMarkRoomReadIsInertOnAnInvitation(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	m = update(t, m, inviteUpdateMsg{invites: []domain.Room{
		{ID: "!inv:x", Name: "Offer", Membership: domain.MembershipInvite, InvitedBy: "@ada:x"},
	}})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, inviteGroupKey)
	next, _ := m.selectRoom(domain.Room{ID: "!inv:x", Name: "Offer", Membership: domain.MembershipInvite})
	m = next

	if _, cmd := press(t, m, keyText("m")); cmd != nil {
		t.Error("m on an invitation should do nothing")
	}
	if b.calls != 0 {
		t.Errorf("backend called %d times for an invitation, want 0", b.calls)
	}
}

// The rail's mark-read confirms, then marks only the group's unread rooms.
func TestMarkGroupReadAsksThenMarksOnlyTheUnread(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	b.result = domain.ReadResult{Marked: 1}
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")

	m, cmd := press(t, m, keyText("m"))
	if cmd != nil {
		t.Error("the question should not act before it is answered")
	}
	if !m.confirm.active() {
		t.Fatal("m on a rail group did not ask")
	}
	if prompt := m.confirmPrompt(); !strings.Contains(prompt, "1 room") || !strings.Contains(prompt, "Work") {
		t.Errorf("question = %q, want the count and the group", prompt)
	}
	// Bravo is in Work but read; Cyan is unread but not in Work.
	if want := []domain.RoomID{"!a:x"}; !reflect.DeepEqual(m.confirm.rooms, want) {
		t.Errorf("captured rooms = %v, want %v", m.confirm.rooms, want)
	}

	m, cmd = press(t, m, keyText("y"))
	if m.confirm.active() {
		t.Error("the question survived its answer")
	}
	if cmd == nil {
		t.Fatal("yes sent no command")
	}
	runCmd(t, cmd)
	if want := []domain.RoomID{"!a:x"}; !reflect.DeepEqual(b.asked, want) {
		t.Errorf("backend asked for %v, want %v", b.asked, want)
	}
}

// Answering no does nothing.
func TestMarkGroupReadCanBeDeclined(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")

	m, _ = press(t, m, keyText("m"))
	if !m.confirm.active() {
		t.Fatal("m on a rail group did not ask")
	}
	m, cmd := press(t, m, keyText("n"))
	if m.confirm.active() || untimed(t, cmd) != nil {
		t.Error("no should close the question and do nothing")
	}
	if b.calls != 0 {
		t.Errorf("backend called %d times after a no, want 0", b.calls)
	}
}

// A group with nothing unread asks nothing.
func TestMarkGroupReadWithNothingUnread(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	m.unread = map[domain.RoomID]domain.Unread{}
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")

	m, cmd := press(t, m, keyText("m"))
	if m.confirm.active() || untimed(t, cmd) != nil {
		t.Error("with nothing unread there is nothing to ask or do")
	}
	if !strings.Contains(m.status(), "nothing unread") {
		t.Errorf("status = %q, want it to say there was nothing to do", m.status())
	}
	if b.calls != 0 {
		t.Errorf("backend called %d times, want 0", b.calls)
	}
}

// The Unread group gathers every unread room and nothing else.
func TestMarkGroupReadOverTheUnreadGroup(t *testing.T) {
	t.Parallel()

	m, _ := reading(t)
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "unread")

	m, _ = press(t, m, keyText("m"))
	want := []domain.RoomID{"!a:x", "!c:x"}
	if !reflect.DeepEqual(m.confirm.rooms, want) {
		t.Errorf("captured rooms = %v, want every unread room %v", m.confirm.rooms, want)
	}
}

// The status reports partial outcomes rather than implying all rooms were marked.
func TestReadStatusReportsWhatHappened(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg  markedReadMsg
		want string
	}{
		"one room marked": {
			msg:  markedReadMsg{label: "Alpha", rooms: 1, result: domain.ReadResult{Marked: 1}},
			want: "marked Alpha read",
		},
		"one room with nothing cached": {
			msg:  markedReadMsg{label: "Alpha", rooms: 1, result: domain.ReadResult{Skipped: 1}},
			want: "open it to mark it read",
		},
		"one room refused": {
			msg:  markedReadMsg{label: "Alpha", rooms: 1, result: domain.ReadResult{Failed: 1}},
			want: "could not mark Alpha read",
		},
		"a group, all marked": {
			msg:  markedReadMsg{label: "Work", rooms: 12, result: domain.ReadResult{Marked: 12}},
			want: "marked 12 rooms in Work read",
		},
		"a group, partly": {
			msg:  markedReadMsg{label: "Work", rooms: 12, result: domain.ReadResult{Marked: 9, Skipped: 2, Failed: 1}},
			want: "2 rooms had nothing cached",
		},
		"a failure names itself": {
			msg:  markedReadMsg{label: "Work", rooms: 12, err: errors.New("daemon is gone")},
			want: "daemon is gone",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := readStatus(tc.msg); !strings.Contains(got, tc.want) {
				t.Errorf("readStatus() = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// Badges clear only via the Unread stream, so a refused receipt leaves them.
func TestMarkReadDoesNotClearBadgesLocally(t *testing.T) {
	t.Parallel()

	m, b := reading(t)
	b.result = domain.ReadResult{Failed: 1}
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next

	m, cmd := press(t, m, keyText("m"))
	m = update(t, m, cmd())
	if !m.unread["!a:x"].HasUnread(true) {
		t.Error("the badge was cleared locally, without the homeserver agreeing")
	}
}

// marker records what MarkRoomUnread was asked for.
type marker struct {
	apitest.Nop
	calls  int
	room   domain.RoomID
	unread bool
	err    error
}

func (mk *marker) MarkRoomUnread(_ context.Context, roomID domain.RoomID, unread bool) error {
	mk.calls++
	mk.room, mk.unread = roomID, unread
	return mk.err
}

// marking returns a model with two rooms, one already marked unread, room list focused.
func marking(t *testing.T) (Model, *marker) {
	t.Helper()
	mk := &marker{}
	m := update(t, New(context.Background(), mk, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!b:x", Marked: true}}})
	m = m.clearStatus()
	m = sized(t, m)
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	return m, mk
}

// M flags a read room; nothing changes locally until the Unread stream says so.
func TestMarkRoomUnreadFlagsTheRoom(t *testing.T) {
	t.Parallel()

	m, mk := marking(t)
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next

	m, cmd := press(t, m, keyText("M"))
	if cmd == nil {
		t.Fatal("M in the room list sent no command")
	}
	msg, ok := cmd().(markedUnreadMsg)
	if !ok {
		t.Fatalf("command returned %T, want markedUnreadMsg", cmd())
	}
	if mk.calls != 1 || mk.room != "!a:x" || !mk.unread {
		t.Errorf("backend asked calls=%d room=%q unread=%v, want 1 !a:x true", mk.calls, mk.room, mk.unread)
	}
	m = update(t, m, msg)
	if m.status() != "marked Alpha unread" {
		t.Errorf("status = %q, want it to name the room", m.status())
	}
}

// The same key clears a mark, whoever set it.
func TestMarkRoomUnreadTogglesOff(t *testing.T) {
	t.Parallel()

	m, mk := marking(t)
	next, _ := m.selectRoom(domain.Room{ID: "!b:x", Name: "Bravo"})
	m = next

	m, cmd := press(t, m, keyText("M"))
	if cmd == nil {
		t.Fatal("M on a marked room sent no command")
	}
	msg := cmd()
	if mk.unread {
		t.Error("M on an already-marked room should clear the mark, not set it again")
	}
	m = update(t, m, msg)
	if got := m.status(); !strings.Contains(got, "no longer marked") {
		t.Errorf("status = %q, want it to say the mark is gone", got)
	}
}

// A marked room badges without a number but counts as unread everywhere.
func TestAMarkedRoomBadgesWithoutANumber(t *testing.T) {
	t.Parallel()

	m, _ := marking(t)
	badge, highlight := m.unreadBadge(domain.Room{ID: "!b:x", Name: "Bravo"})
	if badge != "●" || highlight {
		t.Errorf("marked badge = %q (highlight %v), want a bare dot", badge, highlight)
	}
	if plain, _ := m.unreadBadge(domain.Room{ID: "!a:x", Name: "Alpha"}); plain != "" {
		t.Errorf("an unmarked, read room badged %q", plain)
	}
	if !m.unreadView().tallies(domain.Room{ID: "!b:x", Name: "Bravo"}) {
		t.Error("a marked room should count as unread")
	}
}

// A room with real unread and a mark shows the count.
func TestAMarkNeverReplacesARealCount(t *testing.T) {
	t.Parallel()

	m, _ := marking(t)
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!b:x", Notifications: 4, Marked: true}}})
	if badge, _ := m.unreadBadge(domain.Room{ID: "!b:x", Name: "Bravo"}); badge != "●4" {
		t.Errorf("badge = %q, want the real count", badge)
	}
}
