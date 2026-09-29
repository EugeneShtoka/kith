package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// upgraded builds a room list holding a replaced room, the room that replaced it,
// and one ordinary room to prove the marks and refusals are not universal.
func upgraded(t *testing.T) Model {
	t.Helper()
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!old:x", Name: "Old", Replacement: "!new:x"},
		{ID: "!new:x", Name: "New"},
		{ID: "!plain:x", Name: "Plain"},
	}})
	return sized(t, m.clearStatus())
}

// A replaced room is the one row in the list that looks completely ordinary and is
// not: it opens, it scrolls, and every send is refused because the server raised its
// power levels. The mark is what makes that visible before you type into it.
func TestAReplacedRoomIsMarkedInTheRoomList(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	lines := map[string]string{}
	for _, row := range m.roomRows() {
		lines[string(row.room.ID)] = m.roomListLine(row, 40, false, true)
	}

	if !strings.Contains(lines["!old:x"], replacedMark) {
		t.Errorf("replaced room row = %q, want the %q mark", lines["!old:x"], replacedMark)
	}
	for _, id := range []string{"!new:x", "!plain:x"} {
		if strings.Contains(lines[id], replacedMark) {
			t.Errorf("%s row = %q, want no replacement mark", id, lines[id])
		}
	}
}

// The key is the way out of a dead room, and the replacement is normally a room the
// server already brought this account into — so the common case must open it
// directly rather than ask to join a room we are in.
func TestGoingToTheReplacementOpensARoomWeAreAlreadyIn(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	m = m.selectRoomForTest(t, "!old:x")

	next, _, handled := m.roomStandingAction(actGoReplacement)
	if !handled {
		t.Fatal("actGoReplacement was not handled in the room list")
	}
	got := next
	if got.openRoom != "!new:x" {
		t.Errorf("open room = %q, want !new:x", got.openRoom)
	}
}

// A room that was never upgraded still answers. The key is bound in every room, and
// silence is indistinguishable from a key that did not register.
func TestGoingToTheReplacementOfAnOrdinaryRoomSaysThereIsNone(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	m = m.selectRoomForTest(t, "!plain:x")

	next, _, _ := m.roomStandingAction(actGoReplacement)
	got := next
	if got.openRoom == "!new:x" {
		t.Fatal("an ordinary room jumped to somebody else's replacement")
	}
	if !strings.Contains(got.st.event, "not been replaced") {
		t.Errorf("status = %q, want it to say the room has no replacement", got.st.event)
	}
}

// The homeserver's answer to sending in a replaced room is M_FORBIDDEN, which says
// nothing about why or where to go.
func TestSendingInAReplacedRoomExplainsAndKeepsTheDraft(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	room, ok := m.roomByID("!old:x")
	if !ok {
		t.Fatal("!old:x missing from the list")
	}

	next, cmd := m.sendComposed(room, "hello", false)
	got := next
	if cmd != nil {
		t.Error("a send was issued into a room the server will refuse")
	}
	if !strings.Contains(got.st.event, "replaced") {
		t.Errorf("status = %q, want it to say the room was replaced", got.st.event)
	}
	// The key that gets you out is named, because "this room was replaced" alone
	// leaves you in it.
	if hint := got.keys.keyHint(scopeRooms, actGoReplacement); hint == "" || !strings.Contains(got.st.event, hint) {
		t.Errorf("status = %q, want it to name the key (%q)", got.st.event, hint)
	}
}

// An ordinary room is unaffected: the guard reads one field, and a guard that fires
// on the wrong rooms would stop the client sending anything at all.
func TestSendingInAnOrdinaryRoomIsUntouched(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	room, ok := m.roomByID("!plain:x")
	if !ok {
		t.Fatal("!plain:x missing from the list")
	}

	_, cmd := m.sendComposed(room, "hello", false)
	if cmd == nil {
		t.Error("no send was issued from a room that can be sent to")
	}
}

// selectRoomForTest puts the room-list cursor on one room.
func (m Model) selectRoomForTest(t *testing.T, id domain.RoomID) Model {
	t.Helper()
	room, ok := m.roomByID(id)
	if !ok {
		t.Fatalf("room %s is not in the list", id)
	}
	next, _ := m.selectRoom(room)
	return next
}
