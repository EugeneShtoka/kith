package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlShiftDigit is ctrl+shift with a digit as Bubble Tea decodes it from the Kitty keyboard
// protocol: CSI 49;6u (disambiguation alone, what Bubble Tea asks for) and CSI 49:33;6u
// (with alternate keys) both give the digit as the code, the shifted symbol aside.
func ctrlShiftDigit(digit, shifted rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: digit, ShiftedCode: shifted, Mod: tea.ModCtrl | tea.ModShift}
}

// focusStart is a place the pane keys are pressed from.
type focusStart struct {
	name string
	set  func(Model) Model
}

// The pane keys go straight to their pane from every pane, the composer and the emoji prompt
// included (where they type nothing), and only the composer key leaves the composer typing.
func TestPaneKeysGoStraightToTheirPane(t *testing.T) {
	t.Parallel()

	opened := roomOpenIn(t)
	starts := []focusStart{
		{"rail", func(m Model) Model { m.focus = paneRail; return m }},
		{"rooms", func(m Model) Model { m.focus = paneRooms; return m }},
		{"timeline", func(m Model) Model { m.focus = paneTimeline; return m }},
		{"composer", func(m Model) Model {
			m.focus, m.compose.insertMode = paneTimeline, true
			return m.store(fieldComposer, editor{text: "hi", at: len("hi")})
		}},
		{"emoji prompt", func(m Model) Model {
			m.focus = paneTimeline
			m.compose = m.compose.startReacting()
			return m
		}},
	}
	keys := []struct {
		key    tea.KeyPressMsg
		pane   pane
		insert bool
	}{
		{ctrlShiftDigit('1', '!'), paneRail, false},
		{ctrlShiftDigit('2', '@'), paneRooms, false},
		{ctrlShiftDigit('3', '#'), paneTimeline, false},
		{ctrlShiftDigit('4', '$'), paneTimeline, true},
	}
	for _, start := range starts {
		for _, k := range keys {
			m, _ := press(t, start.set(opened), k.key)
			if m.focus != k.pane || m.compose.insertMode != k.insert || m.compose.reacting {
				t.Errorf("%s from %s: focus %d insert %v reacting %v, want focus %d insert %v",
					k.key.String(), start.name, m.focus, m.compose.insertMode, m.compose.reacting, k.pane, k.insert)
			}
			if got := m.editorFor(fieldComposer).text; start.name == "composer" && got != "hi" {
				t.Errorf("%s from the composer: composer holds %q, want the draft untouched", k.key.String(), got)
			}
		}
	}
}

// Bubble Tea names the press as the binding is written, ctrl+shift before the digit.
func TestPaneKeysAreNamedAsWritten(t *testing.T) {
	t.Parallel()

	for key, want := range map[tea.KeyPressMsg]string{
		ctrlShiftDigit('1', '!'): "ctrl+shift+1",
		ctrlShiftDigit('4', '$'): "ctrl+shift+4",
		ctrlShiftDigit('2', 0):   "ctrl+shift+2",
	} {
		if got := key.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

// With no room open there is no composer: its key leaves the focus where it was.
func TestComposerKeyWithNoRoomOpen(t *testing.T) {
	t.Parallel()

	m := newModel()
	m.focus = paneRooms
	m, _ = press(t, m, ctrlShiftDigit('4', '$'))
	if m.focus != paneRooms || m.compose.insertMode {
		t.Fatalf("focus %d insert %v, want the rooms pane, not typing", m.focus, m.compose.insertMode)
	}
}

// roomOpenIn is a model with Alpha open, its timeline focused, not typing.
func roomOpenIn(t *testing.T) Model {
	t.Helper()
	m := withRooms(t, sized(t, newModel()))
	m, _ = press(t, m, keyCode(tea.KeyTab))
	m, _ = press(t, m, keyCode(tea.KeyEnter))
	if m.openRoom == "" || m.focus != paneTimeline {
		t.Fatalf("openRoom %q focus %d, want a room open in the timeline", m.openRoom, m.focus)
	}
	m.compose = m.compose.left()
	return m
}
