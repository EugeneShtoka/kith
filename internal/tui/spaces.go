package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Filing a room into or out of spaces and tags: one picker, those holding the room
// ticked. Spaces are state events the daemon writes (m.space.child, m.space.parent)
// and the rail follows the refreshed hierarchy; tags are config lists (tagfiling.go).

// openSpacePicker offers the spaces and tags for the selected room, captured now so a
// re-sort cannot redirect the gesture.
func (m Model) openSpacePicker() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	items, checked, ok := m.filingRows(room)
	if !ok {
		return m.say("nothing in [display] filing_spaces matches a space you are in"), nil
	}
	// Last: New space (Matrix rooms only), then New tag, each with the room in it.
	if domain.NetworkOf(string(room.ID)) == domain.ProtocolMatrix {
		items = append(items, pickerItem{label: "New space", value: spaceNew, match: "new space"})
	}
	items = append(items, pickerItem{label: "New tag", value: tagNew, match: "new tag"})
	m.aimedAt.space = room.ID
	m.picker = newCheckedPicker(pickerRoomSpaces, items, checked)
	return m, nil
}

// spaceNew is the space picker's "New space" row; no space ID is spelled so.
const spaceNew = "\x00new space"

// applyRoomSpaces writes only the difference between the shown ticks and the current
// ones; a space not shown was never asked about and must not be unfiled.
func (m Model) applyRoomSpaces(values []string) (Model, tea.Cmd) {
	room := m.aimedAt.space
	rows := m.picker.all // read before closePicker zeroes it
	// A ticked or focused New row asks for a name after the rest is filed; the focused
	// one wins, then a space.
	made := madeHere(m.picker, values)
	m.aimedAt.space = ""
	m = m.closePicker()
	if room == "" {
		return m, nil
	}
	next, cmd := m.fileAsTicked(room, rows, values, made == "")
	switch made {
	case spaceNew:
		next.aimedAt.creating = domain.NewRoom{On: domain.OwnerOf(room), Kind: domain.ChatSpace}
		next.aimedAt.fileInto = room
		next = next.openPrompt(promptNewRoom)
	case tagNew:
		next.choosing.tag = tagEditing{fileRoom: room}
		next = next.openPrompt(promptTagName)
	}
	return next, cmd
}

// madeHere is the chosen New row: the one under the cursor, else a ticked one, space
// before tag; "" for neither.
func madeHere(p picker, values []string) string {
	if at, ok := p.selected(); ok && (at.value == spaceNew || at.value == tagNew) {
		return at.value
	}
	for _, row := range []string{spaceNew, tagNew} {
		if slices.Contains(values, row) {
			return row
		}
	}
	return ""
}

// fileAsTicked files room into and out of rows by their ticks; sayNoChange reports a
// gesture that changed nothing.
func (m Model) fileAsTicked(room domain.RoomID, rows []pickerItem, values []string, sayNoChange bool) (Model, tea.Cmd) {
	want := make(map[domain.SpaceID]bool, len(values))
	for _, v := range values {
		want[domain.SpaceID(v)] = true
	}
	m, tagged, tagCmd := m.fileInTags(room, rows, want)
	var changes []spaceChange
	for _, item := range rows {
		if isTagGroup(item.value) {
			continue
		}
		space, ok := m.spaceByID(domain.SpaceID(item.value))
		if !ok {
			continue
		}
		if ticked := want[space.ID]; spaceHolds(space, room) != ticked {
			changes = append(changes, spaceChange{id: space.ID, name: stripIsolates(item.label), add: ticked})
		}
	}
	if len(changes) == 0 {
		if !tagged && sayNoChange {
			return m.say("no change"), nil
		}
		return m, tagCmd
	}
	name := m.roomNameOf(room)
	m = m.doing("filing " + name + "…")
	return m, tea.Batch(tagCmd, m.fileRoomCmd(room, name, changes))
}

// handleSpaceFiled reports the gesture in one line and refetches the hierarchy if
// anything landed; nothing moves locally.
func (m Model) handleSpaceFiled(msg spaceFiledMsg) (Model, tea.Cmd) {
	m = m.say(msg.summary())
	if msg.landed() == 0 {
		return m, nil
	}
	return m, m.refreshSpacesCmd()
}

func spaceHolds(space domain.Space, roomID domain.RoomID) bool {
	return slices.Contains(space.Children, roomID)
}

// roomNameOf names a room by ID, for messages about a room that may no longer be selected.
func (m Model) roomNameOf(id domain.RoomID) string {
	for i := range m.rooms.all {
		if m.rooms.all[i].ID == id {
			return m.roomName(m.rooms.all[i])
		}
	}
	return string(id)
}

func (m Model) spaceByID(id domain.SpaceID) (domain.Space, bool) {
	for i := range m.rooms.spaces {
		if m.rooms.spaces[i].ID == id {
			return m.rooms.spaces[i], true
		}
	}
	return domain.Space{}, false
}
