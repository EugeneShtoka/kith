package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// dayRoom is a room with three messages on each of four days.
func dayRoom(t *testing.T) Model {
	t.Helper()
	m := sized(t, withRooms(t, newModel()))
	m.openRoom = "!a:x"
	m.focus, m.compose.insertMode = paneTimeline, false
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	var msgs []domain.Message
	for day := range 4 {
		for n := range 3 {
			msgs = append(msgs, domain.Message{
				ID:         domain.EventID("$d" + string(rune('0'+day)) + "m" + string(rune('0'+n))),
				RoomID:     "!a:x",
				Sender:     "@bob:x",
				SenderName: "Bob",
				Body:       "day " + string(rune('0'+day)) + " message " + string(rune('0'+n)),
				Timestamp:  base.AddDate(0, 0, day).Add(time.Duration(n) * time.Hour),
			})
		}
	}
	m = m.setMessages(msgs)
	m.timeline.hist.atStart = true // the room's start is loaded, so no fetch is attempted
	m.timeline.selected = msgs[len(msgs)-1].ID
	return m
}

func pressKey(t *testing.T, m Model, k string) Model {
	t.Helper()
	next, _ := asModel(m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k}))
	return next
}

// Back goes to the start of the current day first, then to the day before.
func TestPrevDayStopsAtTheStartOfTheDayFirst(t *testing.T) {
	m := dayRoom(t)
	if want := "$d3m2"; string(m.timeline.selected) != want {
		t.Fatalf("setup: selected %s, want %s", m.timeline.selected, want)
	}
	m = pressKey(t, m, "[")
	if want := "$d3m0"; string(m.timeline.selected) != want {
		t.Fatalf("first [ selected %s, want the start of day 3 (%s)", m.timeline.selected, want)
	}
	m = pressKey(t, m, "[")
	if want := "$d2m0"; string(m.timeline.selected) != want {
		t.Fatalf("second [ selected %s, want the start of day 2 (%s)", m.timeline.selected, want)
	}
	m = pressKey(t, m, "[")
	if want := "$d1m0"; string(m.timeline.selected) != want {
		t.Fatalf("third [ selected %s, want the start of day 1 (%s)", m.timeline.selected, want)
	}
}

// Forward goes straight to the next day's first message.
func TestNextDayGoesToTheNextDaysFirstMessage(t *testing.T) {
	m := dayRoom(t)
	m.timeline.selected = "$d0m1" // mid-day 0
	m = pressKey(t, m, "]")
	if want := "$d1m0"; string(m.timeline.selected) != want {
		t.Errorf("] selected %s, want %s", m.timeline.selected, want)
	}
	m = pressKey(t, m, "]")
	if want := "$d2m0"; string(m.timeline.selected) != want {
		t.Errorf("second ] selected %s, want %s", m.timeline.selected, want)
	}
}

// A day jump keeps the date separator on screen.
func TestDayJumpShowsTheDateSeparator(t *testing.T) {
	m := dayRoom(t)
	m = pressKey(t, m, "[")
	m = pressKey(t, m, "[") // start of day 2
	lines := m.messageLines(m.msgAreaRows(), m.contentWidth())
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Aug 2026") {
		t.Errorf("no date separator in the viewport after a day jump:\n%s", joined)
	}
	if !strings.Contains(joined, "day 2 message 0") {
		t.Errorf("the day's first message is not in the viewport:\n%s", joined)
	}
}

// The ends say so.
func TestDayJumpAtTheEnds(t *testing.T) {
	m := dayRoom(t)
	m.timeline.selected = "$d0m0" // the oldest message of the oldest day
	m = pressKey(t, m, "[")
	if !strings.Contains(m.status(), "no earlier day") {
		t.Errorf("status after [ at the oldest day = %q", m.status())
	}
	m.timeline.selected = "$d3m2"
	m = pressKey(t, m, "]")
	if !strings.Contains(m.status(), "last day") {
		t.Errorf("status after ] at the newest day = %q", m.status())
	}
}

// The title carries the selected message's day, for when no separator is on screen.
func TestStickyDayInTheTitle(t *testing.T) {
	m := dayRoom(t)
	m.timeline.selected = "$d1m2"
	m = m.scrollToSelection()
	if got := m.stickyDay(); got == "" {
		t.Fatal("no day in the title")
	} else if !strings.Contains(got, "Aug") {
		t.Errorf("title day = %q, want the selected message's date", got)
	}
	m.timeline.selected = "$d3m0"
	m = m.scrollToSelection()
	day3 := m.stickyDay()
	m.timeline.selected = "$d0m0"
	m = m.scrollToSelection()
	if day0 := m.stickyDay(); day0 == day3 {
		t.Errorf("the title showed %q for both day 0 and day 3", day0)
	}
	pane := m.renderTimeline(m.width-railWidth-roomsWidth, m.height-1)
	if !strings.Contains(pane, m.stickyDay()) {
		t.Errorf("the day is not in the drawn title:\n%s", pane)
	}
}

// While typing, the title's day follows the view, not the stale selection.
func TestStickyDayFollowsTheViewWhileTyping(t *testing.T) {
	m := dayRoom(t)
	m.height = 12
	m.timeline.selected = "$d3m2" // newest
	m.compose.insertMode = true
	m.timeline.scroll = 0
	atBottom := m.stickyDay()
	if m.maxScroll() == 0 {
		t.Fatal("setup: the room still fits on screen, so there is nothing to scroll past")
	}

	m.timeline.scroll = m.maxScroll()
	if got := m.stickyDay(); got == atBottom {
		t.Errorf("the title stayed on %q after scrolling to the top while typing", got)
	}
}

// gg shows the first message with its date separator.
func TestSelectOldestShowsTheDateSeparator(t *testing.T) {
	m := dayRoom(t)
	next, _ := m.selectEnd(-1)
	after := next
	joined := strings.Join(after.messageLines(after.msgAreaRows(), after.contentWidth()), "\n")
	if !strings.Contains(joined, "Aug 2026") {
		t.Errorf("no date separator above the first message:\n%s", joined)
	}
	if !strings.Contains(joined, "day 0 message 0") {
		t.Errorf("the first message is not in view:\n%s", joined)
	}
}
