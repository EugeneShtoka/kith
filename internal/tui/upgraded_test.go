package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// upgraded builds a room list with a replaced room, its replacement, and an ordinary
// room as control.
func upgraded(t *testing.T) Model {
	t.Helper()
	m := update(t, starterNew(apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!old:x", Name: "Old", Replacement: mx + "!new:x"},
		{ID: mx + "!new:x", Name: "New"},
		{ID: mx + "!plain:x", Name: "Plain"},
	}})
	return sized(t, m.clearStatus())
}

// A replaced room looks ordinary but every send is refused (the server raised its power
// levels); the mark shows that before you type.
func TestAReplacedRoomIsMarkedInTheRoomList(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	lines := map[string]string{}
	for _, row := range m.roomRows() {
		lines[string(row.room.ID)] = m.roomListLine(row, 40, false, true)
	}

	if !strings.Contains(lines[mx+"!old:x"], replacedMark) {
		t.Errorf("replaced room row = %q, want the %q mark", lines[mx+"!old:x"], replacedMark)
	}
	for _, id := range []string{mx + "!new:x", mx + "!plain:x"} {
		if strings.Contains(lines[id], replacedMark) {
			t.Errorf("%s row = %q, want no replacement mark", id, lines[id])
		}
	}
}

// /replacement opens a replacement the account is already in rather than asking to join.
func TestGoingToTheReplacementOpensARoomWeAreAlreadyIn(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	m = m.selectRoomForTest(t, mx+"!old:x")
	room, _ := m.roomByID(mx + "!old:x")

	handled, next, _ := m.composerCommand("/replacement", room)
	if !handled {
		t.Fatal("/replacement was not taken as a command")
	}
	got := next
	if got.openRoom != mx+"!new:x" {
		t.Errorf("open room = %q, want !new:x", got.openRoom)
	}
}

// A never-upgraded room still answers: silence looks like a command that did nothing.
func TestGoingToTheReplacementOfAnOrdinaryRoomSaysThereIsNone(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	m = m.selectRoomForTest(t, mx+"!plain:x")
	room, _ := m.roomByID(mx + "!plain:x")

	_, next, _ := m.composerCommand("/replacement", room)
	got := next
	if got.openRoom == mx+"!new:x" {
		t.Fatal("an ordinary room jumped to somebody else's replacement")
	}
	if !strings.Contains(got.st.event, "not been replaced") {
		t.Errorf("status = %q, want it to say the room has no replacement", got.st.event)
	}
}

// The homeserver answers a send in a replaced room with a bare M_FORBIDDEN.
func TestSendingInAReplacedRoomExplainsAndKeepsTheDraft(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	room, ok := m.roomByID(mx + "!old:x")
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
	// The way out is named.
	if !strings.Contains(got.st.event, "/replacement") {
		t.Errorf("status = %q, want it to name /replacement", got.st.event)
	}
}

// An ordinary room is unaffected; a guard firing on the wrong rooms would stop all sends.
func TestSendingInAnOrdinaryRoomIsUntouched(t *testing.T) {
	t.Parallel()

	m := upgraded(t)
	room, ok := m.roomByID(mx + "!plain:x")
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
