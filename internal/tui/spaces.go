package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Filing a room into a space and back out: one picker of your spaces, those already
// holding the room ticked, choosing toggles. Both halves are state events the daemon
// writes (m.space.child, m.space.parent); the rail follows the refreshed hierarchy.

// openSpacePicker offers the spaces for the selected room. The room is captured now, so
// a re-sort under the open picker cannot redirect the gesture.
func (m Model) openSpacePicker() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	items, checked := m.fileableSpaces(room.ID)
	if len(items) == 0 {
		if len(m.prefs.display.FilingSpaces) > 0 {
			return m.say("nothing in [display] filing_spaces matches a space you are in"), nil
		}
		return m.say("no spaces to file " + m.roomName(room) + " into"), nil
	}
	m.aimedAt.space = room.ID
	m.picker = newCheckedPicker(pickerRoomSpaces, items, checked)
	return m, nil
}

// fileableSpaces is the picker's rows, in the rail's order under the rail's names.
// Managed spaces (a bridge's, or a room's origin) are left out — they stay browsable
// but filing into them is meaningless — and hidden spaces are absent because they are
// not in the rail; memberships there are untouched since the diff covers shown rows only.
func (m Model) fileableSpaces(room domain.RoomID) ([]pickerItem, map[string]bool) {
	if named := m.prefs.display.FilingSpaces; len(named) > 0 {
		return m.namedSpaces(named, room)
	}
	items := make([]pickerItem, 0, len(m.rail.groups))
	checked := make(map[string]bool, len(m.rail.groups))
	for _, g := range m.rail.groups {
		space, ok := m.spaceForGroup(g)
		if !ok || space.Managed() {
			continue
		}
		items = appendSpaceRow(items, checked, space, g.label, room)
	}
	return items, checked
}

// appendSpaceRow adds one filing row, ticked when the space already holds the room.
func appendSpaceRow(items []pickerItem, checked map[string]bool, space domain.Space, label string, room domain.RoomID) []pickerItem {
	if spaceHolds(space, room) {
		checked[string(space.ID)] = true
	}
	return append(items, pickerItem{
		label: isolate(label),
		value: string(space.ID),
		match: label + " " + space.DisplayName() + " " + string(space.ID),
	})
}

// spaceForGroup resolves a rail entry (keyed by display name) to its space; false for a
// synthetic group, or for a name two spaces share — filing into a guess is worse than
// not offering the row.
func (m Model) spaceForGroup(g group) (domain.Space, bool) {
	var found domain.Space
	hits := 0
	for i := range m.rooms.spaces {
		if m.rooms.spaces[i].DisplayName() == g.key {
			found, hits = m.rooms.spaces[i], hits+1
		}
	}
	return found, hits == 1
}

// applyRoomSpaces writes only the difference between what the picker showed as held and
// what is ticked now. The diff is over the shown rows only: a space the list left out
// was never asked about and must not be unfiled.
func (m Model) applyRoomSpaces(values []string) (Model, tea.Cmd) {
	room := m.aimedAt.space
	rows := m.picker.all // read before closePicker zeroes it
	m.aimedAt.space = ""
	m = m.closePicker()
	if room == "" {
		return m, nil
	}
	want := make(map[domain.SpaceID]bool, len(values))
	for _, v := range values {
		want[domain.SpaceID(v)] = true
	}
	var changes []spaceChange
	for _, item := range rows {
		space, ok := m.spaceByID(domain.SpaceID(item.value))
		if !ok {
			continue
		}
		if ticked := want[space.ID]; spaceHolds(space, room) != ticked {
			changes = append(changes, spaceChange{id: space.ID, name: stripIsolates(item.label), add: ticked})
		}
	}
	if len(changes) == 0 {
		return m.say("no change"), nil
	}
	name := m.roomNameOf(room)
	m = m.doing("filing " + name + "…")
	return m, m.fileRoomCmd(room, name, changes)
}

// handleSpaceFiled reports the whole gesture in one line and, if anything landed,
// refetches the hierarchy — nothing is moved locally.
func (m Model) handleSpaceFiled(msg spaceFiledMsg) (Model, tea.Cmd) {
	m = m.say(msg.summary())
	if msg.landed() == 0 {
		return m, nil
	}
	return m, m.refreshSpacesCmd()
}

// spaceHolds reports whether a space lists this room as a child.
func spaceHolds(space domain.Space, roomID domain.RoomID) bool {
	return slices.Contains(space.Children, roomID)
}

// roomNameOf names a room by ID, for messages about a room that may no longer be
// selected.
func (m Model) roomNameOf(id domain.RoomID) string {
	for i := range m.rooms.all {
		if m.rooms.all[i].ID == id {
			return m.roomName(m.rooms.all[i])
		}
	}
	return string(id)
}

// spaceByID finds a space among the ones we are in.
func (m Model) spaceByID(id domain.SpaceID) (domain.Space, bool) {
	for i := range m.rooms.spaces {
		if m.rooms.spaces[i].ID == id {
			return m.rooms.spaces[i], true
		}
	}
	return domain.Space{}, false
}

// namedSpaces is the picker's rows when [display] filing_spaces names them: in written
// order, with no managed-space inference (the escape hatch for a misjudgment). Entries
// matching nothing are skipped.
func (m Model) namedSpaces(named []string, room domain.RoomID) ([]pickerItem, map[string]bool) {
	items := make([]pickerItem, 0, len(named))
	checked := make(map[string]bool, len(named))
	seen := make(map[domain.SpaceID]bool, len(named))
	for _, entry := range named {
		space, ok := m.spaceByEntry(entry)
		if !ok || seen[space.ID] {
			continue
		}
		seen[space.ID] = true
		items = appendSpaceRow(items, checked, space, m.spaceLabel(space), room)
	}
	return items, checked
}

// spaceByEntry resolves a filing_spaces entry: a space ID, or a unique case-insensitive
// match on its display name or rail label.
func (m Model) spaceByEntry(entry string) (domain.Space, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return domain.Space{}, false
	}
	if space, ok := m.spaceByID(domain.SpaceID(entry)); ok {
		return space, true
	}
	var found domain.Space
	hits := 0
	for i := range m.rooms.spaces {
		if strings.EqualFold(m.rooms.spaces[i].DisplayName(), entry) || strings.EqualFold(m.spaceLabel(m.rooms.spaces[i]), entry) {
			found, hits = m.rooms.spaces[i], hits+1
		}
	}
	return found, hits == 1
}

// spaceLabel is what the rail calls a space (its rename, if any).
func (m Model) spaceLabel(space domain.Space) string {
	for _, g := range m.rail.groups {
		if g.key == space.DisplayName() {
			return g.label
		}
	}
	return space.DisplayName()
}
