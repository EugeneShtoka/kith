package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The wheel over the timeline scrolls it, and the message cursor comes with it — the
// same rule paging follows, for the same reason: a cursor left behind drags the view
// back to itself on the next arrow, so the scroll undoes itself one keypress later.
func TestWheelScrollsTheTimeline(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
	m.timeline.scroll = 0

	// x past both lists is the timeline.
	up, _ := asModel(m.Update(tea.MouseWheelMsg{X: railWidth + roomsWidth + 4, Button: tea.MouseWheelUp}))
	after := up
	if after.timeline.scroll != wheelRows {
		t.Errorf("wheel up scrolled to %d, want %d", after.timeline.scroll, wheelRows)
	}
	from, to, ok := after.selectionRows()
	if !ok {
		t.Fatal("no selection after the wheel")
	}
	if rows := after.msgAreaRows(); to <= after.timeline.scroll || from >= after.timeline.scroll+rows {
		t.Errorf("the wheel left the cursor outside the viewport: rows [%d,%d), viewport [%d,%d)",
			from, to, after.timeline.scroll, after.timeline.scroll+rows)
	}
	down, _ := asModel(after.Update(tea.MouseWheelMsg{X: railWidth + roomsWidth + 4, Button: tea.MouseWheelDown}))
	back := down
	if back.timeline.scroll != 0 {
		t.Errorf("wheel down left the view at %d, want back at the bottom", back.timeline.scroll)
	}
}

// A click is inert on purpose: two ways of saying where you are disagree the moment you
// touch either one, so the pointer never moves the cursor.
func TestClickDoesNothing(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus = paneTimeline
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
	m.timeline.scroll = 12

	clicked, cmd := asModel(m.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft}))
	after := clicked
	if after.timeline.scroll != 12 || after.timeline.selected != m.timeline.selected || after.focus != m.focus {
		t.Error("a click moved something")
	}
	if cmd != nil {
		t.Error("a click asked for work to be done")
	}
}

// Which pane a column belongs to is the whole hit test, and the two boundaries are
// where an off-by-one would send the wheel to the wrong pane.
func TestPaneAtColumns(t *testing.T) {
	m := benchTimelineModel(1)
	for _, tc := range []struct {
		x    int
		want pane
	}{
		{0, paneRail},
		{railWidth - 1, paneRail},
		{railWidth, paneRooms},
		{railWidth + roomsWidth - 1, paneRooms},
		{railWidth + roomsWidth, paneTimeline},
		{1000, paneTimeline},
	} {
		if got := m.paneAt(tc.x); got != tc.want {
			t.Errorf("paneAt(%d) = %v, want %v", tc.x, got, tc.want)
		}
	}
}
