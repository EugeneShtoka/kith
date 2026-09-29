package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Only the wheel is handled: clicks are deliberately inert so the pointer never
// disagrees with the keyboard cursor. It is a setting because mouse reporting disables
// the terminal's own drag-to-select.

// wheelRows is how far one notch moves the timeline; lists move by one row.
const wheelRows = 3

// mouseMode is what the view asks the terminal for; nothing when the setting is off.
func (m Model) mouseMode() tea.MouseMode {
	if !m.prefs.display.UseMouse() {
		return tea.MouseModeNone
	}
	return tea.MouseModeCellMotion
}

// handleMouseWheel scrolls whatever the pointer is over: overlays first, then the pane
// under the column.
func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (Model, tea.Cmd) {
	up := msg.Button == tea.MouseWheelUp
	down := msg.Button == tea.MouseWheelDown
	if !up && !down {
		return m, nil // horizontal wheels have nothing to scroll here
	}
	sign := 1
	if down {
		sign = -1
	}
	switch {
	case m.reader.up():
		return m.scrollHelp(m.reader.scroll - sign*wheelRows), nil
	case m.picker.active():
		n := len(m.picker.items)
		m.picker.cursor = moveCursor(m.picker.cursor, n, -sign, false)
		return m, nil
	case m.search.active:
		return m.moveSearchCursor(-sign)
	}
	switch m.paneAt(msg.X) {
	case paneRail:
		if next, moved := m.rail.moved(-sign); moved {
			m.rail = next
			return m.selectGroup()
		}
		return m, nil
	case paneRooms:
		return m.stepRow(-sign)
	default:
		// The message cursor follows the scroll (as with page keys) except while typing.
		if m.compose.insertMode {
			return m.scrollBy(sign * wheelRows)
		}
		return m.followScroll(m.scrollBy(sign * wheelRows))
	}
}

// paneAt is which pane owns a column; panes have fixed widths left to right.
func (m Model) paneAt(x int) pane {
	switch {
	case x < railWidth:
		return paneRail
	case x < railWidth+roomsWidth:
		return paneRooms
	default:
		return paneTimeline
	}
}
