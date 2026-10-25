package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// portalRooms is a room a bridge put in its own space, also filed by the user into
// a space of their own.
func portalRooms(t *testing.T) Model {
	t.Helper()
	m := update(t, starterNew(apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: mx + "!chat:x", Name: "Someone"},
		{ID: mx + "!other:x", Name: "Someone Else"},
	}})
	return sized(t, update(t, m, spacesMsg{spaces: []domain.Space{
		{
			ID: mx + "!wa:x", Name: "WhatsApp BG", Bridge: domain.ProtocolWhatsApp,
			Children: []domain.RoomID{mx + "!chat:x", mx + "!other:x"},
		},
		{
			ID: mx + "!work:x", Name: "Work", Bridge: domain.ProtocolMatrix,
			Children: []domain.RoomID{mx + "!chat:x"},
		},
	}}).clearStatus())
}

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

// A portal room stays in its bridge space when archived: the bridge created it there.
func TestArchivingKeepsARoomInItsBridgeSpaceAndOnlyItsBridgeSpace(t *testing.T) {
	t.Parallel()

	m := portalRooms(t)
	m, _ = m.toggleTag("Archived", roomByName(t, m, mx+"!chat:x"))

	if !inGroup(t, m, "WhatsApp BG", mx+"!chat:x") {
		t.Error("an archived portal left its bridge's space — the only space it ever lived in")
	}
	// The user's own space stops showing it.
	if inGroup(t, m, "Work", mx+"!chat:x") {
		t.Error("an archived room is still in a space it was filed into by hand")
	}
	// All excludes it too.
	if inGroup(t, m, homeGroupKey, mx+"!chat:x") {
		t.Error("an archived room is still in All")
	}
	if !inGroup(t, m, homeGroupKey, mx+"!other:x") {
		t.Error("archiving one room removed another from All")
	}
}

func TestArchivingTheOpenRoomMovesTheCursorToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)

	// Alpha is first, Bravo second.
	next, _ := m.selectRoom(roomByName(t, m, mx+"!a:x"))
	m = next
	m, _ = m.toggleTag("Archived", roomByName(t, m, mx+"!a:x"))

	if m.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x — the room after the one that left", m.openRoom)
	}
	// The status message survives the move.
	if !strings.Contains(m.status(), "Alpha is in Archived") {
		t.Errorf("status = %q, want the archive message to outlive the navigation", m.status())
	}
}

// With no next room the cursor takes the previous one, not row zero.
func TestArchivingTheLastRoomMovesTheCursorBackwards(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)

	next, _ := m.selectRoom(roomByName(t, m, mx+"!b:x"))
	m = next
	m, _ = m.toggleTag("Archived", roomByName(t, m, mx+"!b:x"))

	if m.openRoom != mx+"!a:x" {
		t.Errorf("open room = %q, want !a:x — there is no next room, so the previous one", m.openRoom)
	}
}

// A settings change that takes nothing out of the list must not move anything.
func TestAnUnrelatedSettingsChangeLeavesTheCursorAlone(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	next, _ := m.selectRoom(roomByName(t, m, mx+"!b:x"))
	m = next

	display := m.prefs.display
	display.MaxNameLength = 22
	next, _ = m.applyDisplay(display, "renamed")
	m = next

	if m.openRoom != mx+"!b:x" {
		t.Errorf("open room = %q, want !b:x — nothing left the list", m.openRoom)
	}
}

// Removal from a space arrives as a hierarchy change, a different path than
// archiving, and needs the same cursor rule.
func TestRemovingARoomFromASpaceMovesTheCursorToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := portalRooms(t)
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "WhatsApp BG")
	next, _ := m.selectRoom(roomByName(t, m, mx+"!chat:x"))
	m = next

	// The space no longer lists it, as after `S` and a refresh.
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{
			ID: mx + "!wa:x", Name: "WhatsApp BG", Bridge: domain.ProtocolWhatsApp,
			Children: []domain.RoomID{mx + "!other:x"},
		},
	}})

	if m.openRoom != mx+"!other:x" {
		t.Errorf("open room = %q, want !other:x — the room the removed one left behind", m.openRoom)
	}
}

// A room left or kicked from also hands the cursor to its neighbor.
func TestARoomLeavingAltogetherAlsoHandsOverToItsNeighbor(t *testing.T) {
	t.Parallel()

	m := counting(t, config.Display{})
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	next, _ := m.selectRoom(roomByName(t, m, mx+"!b:x"))
	m = next

	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})

	if m.openRoom != mx+"!a:x" {
		t.Errorf("open room = %q, want !a:x", m.openRoom)
	}
}

// A cold start has no position to keep, so it lands at the top.
func TestAColdStartStillLandsOnTheFirstRoom(t *testing.T) {
	t.Parallel()

	m := sized(t, starterNew(apitest.Nop{}, config.Display{}))
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"},
	}})

	if m.openRoom != mx+"!a:x" {
		t.Errorf("open room = %q, want !a:x on a cold start", m.openRoom)
	}
}
