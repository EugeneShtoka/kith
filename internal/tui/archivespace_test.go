package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// portalRooms builds the shape this went wrong on: a room a bridge put in its own
// space, which the user also filed into a space of their own.
func portalRooms(t *testing.T) Model {
	t.Helper()
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!chat:x", Name: "Someone"},
		{ID: "!other:x", Name: "Someone Else"},
	}})
	return sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{
			ID: "!wa:x", Name: "WhatsApp BG", Bridge: domain.ProtocolWhatsApp,
			Children: []domain.RoomID{"!chat:x", "!other:x"},
		},
		{
			ID: "!work:x", Name: "Work", Bridge: domain.ProtocolMatrix,
			Children: []domain.RoomID{"!chat:x"},
		},
	}}).clearStatus())
}

// inGroup reports whether a rail group's filter admits a room.
func inGroup(t *testing.T, m Model, key string, id domain.RoomID) bool {
	t.Helper()
	room, ok := m.roomByID(id)
	if !ok {
		t.Fatalf("room %s is not in the list", id)
	}
	for _, g := range m.rail.groups {
		if g.key == key {
			return g.admits(m.unreadView(), room)
		}
	}
	t.Fatalf("no rail group %q in %+v", key, m.rail.groups)
	return false
}

// A portal room did not get into its network's space by being filed there — the bridge
// created it there, and that is the only place it has ever lived.
func TestArchivingKeepsARoomInItsBridgeSpaceAndOnlyItsBridgeSpace(t *testing.T) {
	t.Parallel()

	m := portalRooms(t)
	display := m.prefs.display
	display.Archived = []string{"!chat:x"}
	next, _ := m.applyDisplay(display, "archived")
	m = next

	if !inGroup(t, m, "WhatsApp BG", "!chat:x") {
		t.Error("an archived portal left its bridge's space — the only space it ever lived in")
	}
	// And the space the user filed it into does stop showing it, which is what
	// archiving is for.
	if inGroup(t, m, "Work", "!chat:x") {
		t.Error("an archived room is still in a space it was filed into by hand")
	}
	// All still excludes it: the clutter archiving removes.
	if inGroup(t, m, "home", "!chat:x") {
		t.Error("an archived room is still in All")
	}
	// The room next to it is untouched by any of this.
	if !inGroup(t, m, "home", "!other:x") {
		t.Error("archiving one room removed another from All")
	}
}

// When the room under the cursor does leave the list, the selection goes to its
// neighbor.
func TestArchivingTheOpenRoomMovesTheCursorToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")

	// Alpha is first, Bravo second.
	next, _ := m.selectRoom(roomByName(t, m, "!a:x"))
	m = next
	display := m.prefs.display
	display.Archived = []string{"!a:x"}
	next, _ = m.applyDisplay(display, "archived Alpha")
	m = next

	if m.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x — the room after the one that left", m.openRoom)
	}
	// And what you did survives the move it caused.
	if !strings.Contains(m.status(), "archived Alpha") {
		t.Errorf("status = %q, want the archive message to outlive the navigation", m.status())
	}
}

// With no next room the cursor takes the previous one, which is the other half of the
// rule and the case a naive clamp gets wrong by wrapping to zero.
func TestArchivingTheLastRoomMovesTheCursorBackwards(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")

	next, _ := m.selectRoom(roomByName(t, m, "!b:x"))
	m = next
	display := m.prefs.display
	display.Archived = []string{"!b:x"}
	next, _ = m.applyDisplay(display, "archived Bravo")
	m = next

	if m.openRoom != "!a:x" {
		t.Errorf("open room = %q, want !a:x — there is no next room, so the previous one", m.openRoom)
	}
}

// A settings change that takes nothing out of the list must not move anything.
func TestAnUnrelatedSettingsChangeLeavesTheCursorAlone(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	next, _ := m.selectRoom(roomByName(t, m, "!b:x"))
	m = next

	display := m.prefs.display
	display.MaxNameLength = 22
	next, _ = m.applyDisplay(display, "renamed")
	m = next

	if m.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x — nothing left the list", m.openRoom)
	}
}

// Taking a room out of a space arrives as a hierarchy change, not a room-list one, so
// it reaches the cursor by a different path than archiving does — and needs the same
// rule.
func TestRemovingARoomFromASpaceMovesTheCursorToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := portalRooms(t)
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "WhatsApp BG")
	next, _ := m.selectRoom(roomByName(t, m, "!chat:x"))
	m = next

	// The space no longer lists it — what `S` produces, once the refresh comes back.
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{
			ID: "!wa:x", Name: "WhatsApp BG", Bridge: domain.ProtocolWhatsApp,
			Children: []domain.RoomID{"!other:x"},
		},
	}})

	if m.openRoom != "!other:x" {
		t.Errorf("open room = %q, want !other:x — the room the removed one left behind", m.openRoom)
	}
}

// A room leaving entirely — left, kicked — is the third path, and the top of the list
// is not where the cursor belongs there either.
func TestARoomLeavingAltogetherAlsoHandsOverToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	next, _ := m.selectRoom(roomByName(t, m, "!b:x"))
	m = next

	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})

	if m.openRoom != "!a:x" {
		t.Errorf("open room = %q, want !a:x", m.openRoom)
	}
}

// A cold start has no previous position to preserve, so it still lands at the top — the
// rule is about a room *leaving*, not about every list arriving.
func TestAColdStartStillLandsOnTheFirstRoom(t *testing.T) {
	t.Parallel()

	m := sized(t, New(context.Background(), apitest.Nop{}, config.Display{}))
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"},
	}})

	if m.openRoom != "!a:x" {
		t.Errorf("open room = %q, want !a:x on a cold start", m.openRoom)
	}
}
