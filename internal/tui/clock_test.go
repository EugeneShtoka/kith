package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Choosing how times and dates are written redraws the open timeline at once: rows
// cached in the old format are not reused, and with the wider times of a 12-hour or
// Korean clock every row's name still starts at one column and no row overflows.
func TestAChosenClockRedrawsTheTimeline(t *testing.T) {
	t.Parallel()
	evening := time.Date(2025, 3, 5, 21, 30, 0, 0, time.Local)
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = "!a:x"
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "dinner?", Timestamp: evening},
		{ID: "$2", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "at six", Timestamp: evening.Add(35 * time.Minute)},
	}}})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "21:30") || !strings.Contains(view, "Wed, 05 Mar 2025") {
		t.Fatalf("the default clock is not drawn:\n%s", view)
	}

	for _, tc := range []struct {
		style, long, wantTime, wantDay string
	}{
		{"12h", "DDDD, D MMMM YYYY", "9:30 PM", "Wednesday, 5 March 2025"},
		{"ko", "YYYY년 M월 D일", "오후 9:30", "2025년 3월 5일"},
	} {
		display := m.conf.base.Display
		display.TimeFormat, display.LongDateFormat = tc.style, tc.long
		next, _ := m.applyDisplay(display, "clock")
		m = next
		view := ansi.Strip(next.View().Content)
		if !strings.Contains(view, tc.wantTime) || !strings.Contains(view, tc.wantDay) {
			t.Errorf("%s: the timeline still shows the old clock:\n%s", tc.style, view)
		}
		width, nameAt := next.contentWidth(), -1
		for i, row := range next.layoutRows() {
			plain := ansi.Strip(row)
			if got := ansi.StringWidth(row); got > width {
				t.Errorf("%s: row %d is %d cells in a %d-cell pane: %q", tc.style, i, got, width, plain)
			}
			if before, _, found := strings.Cut(plain, "Maya"); found {
				col := ansi.StringWidth(before)
				if nameAt >= 0 && col != nameAt {
					t.Errorf("%s: row %d's name starts at %d, the one above at %d: %q", tc.style, i, col, nameAt, plain)
				}
				nameAt = col
			}
		}
	}
}
