package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrl+l clears and redraws from any mode, typing nothing.
func TestRedrawKeyWorksEverywhere(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	for _, mode := range []struct {
		name  string
		setup func(Model) Model
	}{
		{"browsing", func(m Model) Model { m.focus = paneRooms; return m }},
		{"in the timeline", func(m Model) Model { m.focus = paneTimeline; return m }},
		{"while typing", func(m Model) Model { m.focus = paneTimeline; m.compose.insertMode = true; return m }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			next, cmd := press(t, mode.setup(m), tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatal("ctrl+l produced no command")
			}
			// The message type is unexported; an empty struct compares by value.
			if cmd() != tea.ClearScreen() {
				t.Errorf("ctrl+l returned %T, want a clear", cmd())
			}
			if next.compose.input != "" {
				t.Errorf("input = %q, want ctrl+l to type nothing", next.compose.input)
			}
		})
	}
}

// Moving to another room repaints; re-selecting the open room (which happens on
// every re-sort) does not.
func TestRoomChangeRepaintsButReselectingDoesNot(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!b:x" // somewhere else, so the next selection is a real move
	mdl, cmd := m.selectRoom(m.rooms.all[0])
	if !hasClear(t, cmd) {
		t.Error("opening a different room should repaint")
	}

	_, cmd = mdl.selectRoom(mdl.rooms.all[0]) // the same room again
	if hasClear(t, cmd) {
		t.Error("re-selecting the open room should not repaint")
	}
}

// hasClear reports whether a command, or any in its batch, clears the screen.
func hasClear(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	if msg == tea.ClearScreen() {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if hasClear(t, c) {
				return true
			}
		}
	}
	return false
}
