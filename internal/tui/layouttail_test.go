package tui

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// layoutIndexed is the reference layout: every loaded message laid out top to bottom,
// with each message's row span, and no window. The pane draws through layoutWindow;
// what it draws must be a slice of this.
func (m Model) layoutIndexed() ([]string, map[domain.EventID][2]int) {
	_, w := m.walkFor()
	m.extendTo(w, math.MaxInt)
	rows := m.topAnchorRows(w)
	spans := make(map[domain.EventID][2]int, len(w.msgs))
	for i := range w.msgs {
		msgRows, rule := m.cachedRows(w, i)
		if rule.any() {
			rows = append(rows, m.ruleAboveRow(rule, w.msgs[i].Timestamp, w.width))
		}
		from := len(rows)
		rows = append(rows, msgRows...)
		if id := w.msgs[i].ID; id != "" {
			spans[id] = [2]int{from, len(rows)}
		}
	}
	return rows, spans
}

// layoutRows is the reference layout's rows.
func (m Model) layoutRows() []string {
	rows, _ := m.layoutIndexed()
	return rows
}

// Scrolling must show the same rows it showed before the layout was windowed: the
// viewport is the thing the user actually sees, so it is checked as a whole rather than
// only through the layout beneath it.
func TestMessageLinesUnchangedByWindowing(t *testing.T) {
	m := benchTimelineModel(300)
	rows := m.msgAreaRows()
	width := m.contentWidth()
	full, _ := m.layoutIndexed()
	for _, scroll := range []int{0, 1, 5, 40, 100, len(full) - rows, len(full)} {
		if scroll < 0 {
			continue
		}
		m.timeline.scroll = scroll
		got := m.messageLines(rows, width)
		// The oracle: the same arithmetic over the unwindowed layout.
		bottom := min(max(len(full)-scroll, 0), len(full))
		top := max(bottom-rows, 0)
		visible := full[top:bottom]
		want := make([]string, 0, rows)
		for range rows - len(visible) {
			want = append(want, "")
		}
		for _, r := range visible {
			want = append(want, clamp(r, width))
		}
		if len(got) != len(want) {
			t.Fatalf("scroll %d: %d lines, want %d", scroll, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("scroll %d, line %d:\n got  %q\n want %q", scroll, i, got[i], want[i])
			}
		}
	}
}

// The viewport cut at both ends must be exactly the slice of the whole layout it claims
// to be. The rooms bracket where a windowed layout goes wrong first: one or no message,
// dividers on every message or none (the window's first message is where a divider is
// invented or lost), and a thread summary anchored above everything, which only a
// window reaching the top may draw.
func TestLayoutWindowMatchesFullSlice(t *testing.T) {
	rooms := map[string]Model{}
	for _, n := range []int{1, 3, 40, 300} {
		rooms[fmt.Sprintf("%d messages", n)] = benchTimelineModel(n)
	}
	empty := benchTimelineModel(0)
	empty = empty.setMessages(nil)
	rooms["empty"] = empty
	for _, name := range []string{"same-day", "every-day"} {
		m := benchTimelineModel(60)
		for i := range m.timeline.messages {
			if name == "same-day" {
				m.timeline.messages[i].Timestamp = m.timeline.messages[0].Timestamp
			} else {
				m.timeline.messages[i].Timestamp = m.timeline.messages[0].Timestamp.AddDate(0, 0, i)
			}
		}
		rooms[name] = m
	}
	anchored := benchTimelineModel(200)
	msgs := slices.Clone(anchored.timeline.messages)
	msgs[0].ThreadRoot = domain.EventID("$gone") // the oldest: nothing loaded above it to hang from
	anchored = anchored.setMessages(msgs)
	if _, w := anchored.walkFor(); len(anchored.topAnchorRows(w)) == 0 {
		t.Fatal("setup: the thread whose root is not loaded draws no row above the top")
	}
	rooms["anchored above the top"] = anchored

	for name, m := range rooms {
		t.Run(name, func(t *testing.T) {
			full, _ := m.layoutIndexed()
			for _, count := range []int{1, 5, 20, 43} {
				for skip := 0; skip <= len(full)+2; skip++ {
					got, owners := m.layoutWindow(skip, count)
					if len(owners) != len(got) {
						t.Fatalf("skip=%d count=%d: %d rows but %d owners — the gutter would number the wrong messages",
							skip, count, len(got), len(owners))
					}
					bottom := min(max(len(full)-skip, 0), len(full))
					top := max(bottom-count, 0)
					want := full[top:bottom]
					if len(got) != len(want) {
						t.Fatalf("skip=%d count=%d: %d rows, want %d", skip, count, len(got), len(want))
					}
					for i := range want {
						if got[i] != want[i] {
							t.Fatalf("skip=%d count=%d row %d:\n got  %q\n want %q", skip, count, i, got[i], want[i])
						}
					}
				}
			}
		})
	}
}
