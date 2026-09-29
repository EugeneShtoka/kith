package tui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// onScreen is what the timeline shows now: its rows, counted from the scroll offset.
func onScreen(m Model) []string {
	rows, _ := m.layoutWindow(m.timeline.scroll, m.msgAreaRows())
	return stripAll(rows)
}

func stripAll(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = stripStyles(r)
	}
	return out
}

// conversation is n messages in !a:x, a minute apart, some threaded under $root.
func conversation(n int, threaded bool) []domain.Message {
	msgs := make([]domain.Message, 0, n+1)
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	if threaded {
		msgs = append(msgs, domain.Message{ID: "$root", RoomID: "!a:x", Sender: "@bob:x", Body: "the question", Timestamp: start})
	}
	for i := range n {
		m := domain.Message{
			ID: domain.EventID(fmt.Sprintf("$m%02d", i)), RoomID: "!a:x", Sender: "@bob:x",
			Body: fmt.Sprintf("message number %d", i), Timestamp: start.Add(time.Duration(i+1) * time.Minute),
		}
		if threaded {
			m.ThreadRoot = "$root"
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// Scrolled up, a live message arrives below: what is on screen stays where it is.
func TestALiveMessageKeepsAScrolledViewStill(t *testing.T) {
	t.Parallel()

	m := inRoom(t, apitest.Nop{}, domain.TimelinePage{Messages: conversation(80, false)})
	m.timeline.scroll = 12
	m = settled(m)
	before := onScreen(m)

	m = update(t, m, incomingMsg{message: domain.Message{
		ID: "$new", RoomID: "!a:x", Sender: "@bob:x", Body: "just arrived",
		Timestamp: time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC),
	}})
	if after := onScreen(m); !slices.Equal(after, before) {
		t.Errorf("the view moved when a message arrived below it:\nbefore %q\nafter  %q", before, after)
	}
}

// Scrolled up in a thread, older replies arrive above: what is on screen stays where
// it is (the offset counts from the newest row, so rows above move nothing).
func TestOlderRepliesKeepAScrolledThreadStill(t *testing.T) {
	t.Parallel()

	msgs := conversation(80, true)
	m := inRoom(t, apitest.Nop{}, domain.TimelinePage{Messages: append(msgs[:1:1], msgs[41:]...)})
	next, _ := m.openThread()
	m = next
	m.timeline.scroll = 12
	m = settled(m)
	before := onScreen(m)

	m = update(t, m, threadPageMsg{roomID: "!a:x", root: "$root", page: domain.TimelinePage{Messages: msgs[1:41], Next: "older"}})
	if after := onScreen(m); !slices.Equal(after, before) {
		t.Errorf("the view jumped when older replies arrived above it:\nbefore %q\nafter  %q", before, after)
	}
}
