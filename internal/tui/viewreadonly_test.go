package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// View only reads. Everything the timeline caches (derived answers, running row
// totals, rendered rows) is written by Update, so rendering is safe from any goroutine
// and a frame never decides state. Checked after each step of a session that moves
// every input the row cache keys on.
func TestViewLeavesTheSharedCacheAlone(t *testing.T) {
	t.Parallel()

	m := benchTimelineModel(300)
	last := m.timeline.messages[len(m.timeline.messages)-1]
	target := m.timeline.messages[len(m.timeline.messages)-3].ID
	steps := []struct {
		name string
		msg  tea.Msg
	}{
		{"the first update", settleMsg{}},
		{"a resize", tea.WindowSizeMsg{Width: 170, Height: 40}},
		{"a line up", keyText("k")},
		{"a page up", tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{"deep in the room", tea.KeyPressMsg{Code: tea.KeyPgUp}},
		{"back to the newest", keyText("G")},
		{"a live message", incomingMsg{message: domain.Message{
			ID: "$live", RoomID: last.RoomID, Sender: benchSenders[2],
			Body: "just arrived", Timestamp: last.Timestamp.Add(time.Minute),
		}}},
		{"a reaction", reactionUpdateMsg{u: domain.ReactionUpdate{Reaction: domain.Reaction{
			ID: "$r1", RoomID: last.RoomID, Target: target, Sender: benchSenders[3], Key: "👍",
		}}}},
		{"insert mode", keyText("i")},
		{"typing", keyText("x")},
		{"back to normal", tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"the widest terminal", tea.WindowSizeMsg{Width: 240, Height: 60}},
		{"search", keyText("/")},
		{"a search term", keyText("o")},
		{"search closed", tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"settings picker", keyText(",")},
		{"picker closed", tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"focus left", keyText("h")},
		{"room list move", keyText("j")},
		{"focus back", tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"too small", tea.WindowSizeMsg{Width: 20, Height: 5}},
		{"big again", tea.WindowSizeMsg{Width: 200, Height: 50}},
		{"a line up again", keyText("k")},
	}
	for _, step := range steps {
		next, _ := asModel(m.Update(step.msg))
		m = next
		before := fmt.Sprintf("%#v", *m.derived)
		_ = m.View()
		if after := fmt.Sprintf("%#v", *m.derived); after != before {
			i := 0
			for i < len(before) && i < len(after) && before[i] == after[i] {
				i++
			}
			t.Fatalf("after %s, View wrote the shared cache at …%s… (was …%s…)", step.name,
				after[max(i-120, 0):min(i+200, len(after))], before[max(i-120, 0):min(i+200, len(before))])
		}
	}
}

// A reaction that makes an on-screen message taller is drawn in the same frame, with
// the rows around it where a fresh layout puts them, not placed by the old heights.
func TestAHeightChangeOnScreenIsDrawnRightAway(t *testing.T) {
	t.Parallel()

	m := settled(benchTimelineModel(300))
	next, _ := asModel(m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp}))
	m = next
	for n := range 30 {
		// A message the window shows now, a different one each time.
		_, owners := m.layoutWindow(m.timeline.scroll, m.msgAreaRows())
		var target domain.Message
		for _, o := range owners {
			if o != noOwner && len(m.timeline.reactions[m.derived.shown[o].ID]) == 0 {
				target = m.derived.shown[o]
				break
			}
		}
		if target.ID == "" {
			break
		}
		next, _ = asModel(m.Update(reactionUpdateMsg{u: domain.ReactionUpdate{Reaction: domain.Reaction{
			ID: domain.EventID(fmt.Sprintf("$r%d", n)), RoomID: target.RoomID,
			Target: target.ID, Sender: benchSenders[3], Key: "👍",
		}}}))
		m = next
		got := stripStyles(m.View().Content)
		fresh := m
		fresh.derived = newDerivedCache()
		fresh = fresh.primeTimeline()
		if want := stripStyles(fresh.View().Content); got != want {
			gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
			for j := range min(len(gl), len(wl)) {
				if gl[j] != wl[j] {
					t.Fatalf("after reacting to %s, line %d:\n got  %q\n want %q", target.ID, j, gl[j], wl[j])
				}
			}
			t.Fatalf("after reacting to %s the frame differs from a fresh layout", target.ID)
		}
	}
}

// Scrolled up, a message below the window that grows (a reaction, here on several)
// leaves the rows on screen exactly where they were, and the layout agrees with a fresh
// one at the scroll it moved to.
func TestAHeightChangeBelowTheWindowKeepsTheViewStill(t *testing.T) {
	t.Parallel()

	m := settled(benchTimelineModel(300))
	for range 3 {
		next, _ := asModel(m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp}))
		m = next
	}
	before := stripStyles(m.View().Content)
	scroll := m.timeline.scroll

	// Every message below the window gets a reaction, in one message each.
	_, owners := m.layoutWindow(m.timeline.scroll, m.msgAreaRows())
	lowest := owners[len(owners)-1]
	below := m.derived.shown[lowest+1:]
	for n, target := range below {
		next, _ := asModel(m.Update(reactionUpdateMsg{u: domain.ReactionUpdate{Reaction: domain.Reaction{
			ID: domain.EventID(fmt.Sprintf("$below%d", n)), RoomID: target.RoomID,
			Target: target.ID, Sender: benchSenders[3], Key: "👍",
		}}}))
		m = next
	}
	if m.timeline.scroll <= scroll {
		t.Fatalf("scroll = %d, was %d: the rows added below were not taken up", m.timeline.scroll, scroll)
	}
	if after := stripStyles(m.View().Content); after != before {
		t.Fatal("the rows on screen moved when messages below them grew")
	}
	fresh := m
	fresh.derived = newDerivedCache()
	fresh = fresh.primeTimeline()
	if want := stripStyles(fresh.View().Content); stripStyles(m.View().Content) != want {
		t.Fatal("the frame differs from a fresh layout at the new scroll")
	}
}

// A Model the cache was not primed for (an older copy, from before a message arrived)
// is drawn on a scratch cache: correctly, and without touching the shared one, which
// holds the newer message set.
func TestViewOfAnOlderCopyLeavesTheSharedCacheAlone(t *testing.T) {
	t.Parallel()

	older := settled(benchTimelineModel(300))
	want := stripStyles(older.View().Content)
	last := older.timeline.messages[len(older.timeline.messages)-1]
	newer, _ := asModel(older.Update(incomingMsg{message: domain.Message{
		ID: "$live", RoomID: last.RoomID, Sender: benchSenders[2],
		Body: "just arrived", Timestamp: last.Timestamp.Add(time.Minute),
	}}))
	before := fmt.Sprintf("%#v", *newer.derived)
	if got := stripStyles(older.View().Content); got != want {
		t.Error("the older copy draws differently from when it was current")
	}
	if after := fmt.Sprintf("%#v", *newer.derived); after != before {
		t.Error("drawing an older copy wrote the shared cache")
	}
}
