package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Editing the layout from the app: renaming rooms, threads and rail groups, and
// rearranging the rail, by pointing at the thing (room IDs and group keys are not shown).

// renameRoom opens the prompt for the selected room's displayed name, prefilled.
func (m Model) renameRoom() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	m.aimedAt.renamingRoom = room.ID
	m = m.openPromptWith(promptRoomName, m.roomLabel(room))
	return m, nil
}

// submitRoomName writes the room alias; an empty name clears the override.
func (m Model) submitRoomName(name string) (Model, tea.Cmd) {
	roomID := m.aimedAt.renamingRoom
	m.aimedAt.renamingRoom = ""
	if roomID == "" {
		return m, nil
	}
	room, ok := m.roomByID(roomID)
	if !ok {
		return m, nil
	}
	display := m.prefs.display
	display.Names = config.SetName(display.Names, string(roomID), strings.TrimSpace(name))
	if strings.TrimSpace(name) == "" {
		return m.applyDisplay(display, "cleared the name override — showing "+isolate(room.DisplayName()))
	}
	return m.applyDisplay(display, "renamed to "+isolate(strings.TrimSpace(name)))
}

// renameThread opens the prompt for a thread's name, prefilled with the current
// (often guessed) name.
func (m Model) renameThread(root domain.EventID) (Model, tea.Cmd) {
	if root == "" {
		return m, nil
	}
	m.aimedAt.renamingThread = root
	m = m.openPromptWith(promptThreadName, m.threadName(root))
	return m, nil
}

// submitThreadName writes the thread alias; an empty name clears the override.
func (m Model) submitThreadName(name string) (Model, tea.Cmd) {
	root := m.aimedAt.renamingThread
	m.aimedAt.renamingThread = ""
	if root == "" {
		return m, nil
	}
	display := m.prefs.display
	display.Names = config.SetName(display.Names, config.NameTargetThread+string(root), strings.TrimSpace(name))
	if strings.TrimSpace(name) == "" {
		return m.applyDisplay(display, "cleared the thread's name")
	}
	return m.applyDisplay(display, "named this thread "+isolate(strings.TrimSpace(name)))
}

// renameGroup opens the prompt for the selected rail group's label, prefilled.
func (m Model) renameGroup() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if isTagGroup(entry.key) {
		// A tag's name is what everything refers to it by: change it in its [[tag]].
		m = m.say("a tag is named in its [[tag]] block: " + isolate(entry.label))
		return m, nil
	}
	m.aimedAt.renamingGroup = entry.key
	m = m.openPromptWith(promptGroupName, entry.label)
	return m, nil
}

// submitGroupName writes the rail rename; an empty name clears the override.
func (m Model) submitGroupName(label string) (Model, tea.Cmd) {
	key := m.aimedAt.renamingGroup
	m.aimedAt.renamingGroup = ""
	if key == "" {
		return m, nil
	}
	display := m.prefs.display
	label = strings.TrimSpace(label)
	display.Names = config.SetName(display.Names, config.GroupTarget(key), label)
	if label == "" {
		return m.applyDisplay(display, "cleared the label override for "+key)
	}
	return m.applyDisplay(display, "renamed to "+isolate(label))
}

// currentGroup is the rail group under the cursor.
func (m Model) currentGroup() (group, bool) { return m.rail.at() }
