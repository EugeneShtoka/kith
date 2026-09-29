package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Paging and the timeline ends move the view.
func TestScrollKeysCarryTheSelection(t *testing.T) {
	keys := map[string]tea.KeyPressMsg{
		"pgup": {Code: tea.KeyPgUp},
		"home": {Code: tea.KeyHome},
		"end":  {Code: tea.KeyEnd},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			m := benchTimelineModel(400)
			m.focus, m.compose.insertMode = paneTimeline, false
			// Start at the bottom with the newest message selected, as opening a room
			// leaves it.
			m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
			m.timeline.scroll = 0
			if name == "end" {
				// end goes to the newest, so start away from it for the move to mean
				// anything.
				m.timeline.scroll = 200
				m = m.keepSelectionVisible()
			}

			moved, _ := asModel(m.Update(key))
			after := moved
			if after.timeline.scroll == m.timeline.scroll {
				t.Fatalf("%s did not move the view (scroll stayed %d)", name, m.timeline.scroll)
			}
			// The selection must now be inside the viewport.
			from, to, ok := after.selectionRows()
			if !ok {
				t.Fatal("no selection after the move")
			}
			rows := after.msgAreaRows()
			if to <= after.timeline.scroll || from >= after.timeline.scroll+rows {
				t.Errorf("%s left the cursor outside the viewport: rows [%d,%d), viewport [%d,%d)",
					name, from, to, after.timeline.scroll, after.timeline.scroll+rows)
			}
			// And an arrow afterwards must not spring the view back.
			nudged, _ := asModel(after.Update(tea.KeyPressMsg{Code: 'k', Text: "k"}))
			back := nudged
			if delta := back.timeline.scroll - after.timeline.scroll; delta < -rows || delta > rows {
				t.Errorf("%s then k moved the view by %d rows, want it to stay put", name, delta)
			}
		})
	}
}

// Scrolling while typing must not move the cursor: there is none on screen, and the
// bindings are documented as view-only so they keep working in insert mode.
func TestScrollWhileTypingLeavesSelection(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, true
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
	before := m.timeline.selected

	moved, _ := asModel(m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp}))
	after := moved
	if after.timeline.scroll == 0 {
		t.Fatal("pgup did not scroll while typing")
	}
	if after.timeline.selected != before {
		t.Errorf("typing + pgup moved the selection from %s to %s", before, after.timeline.selected)
	}
}

// home is "go to the beginning", which means the *first* message — not the nearest
// visible one.
func TestHomeSelectsTheFirstMessage(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.hist.atStart = true // the room's start is loaded, so nothing is fetched
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID

	next, _ := asModel(m.Update(tea.KeyPressMsg{Code: tea.KeyHome}))
	after := next

	oldest := after.shownMessages()[0].ID
	if after.timeline.selected != oldest {
		t.Errorf("home selected %s, want the oldest message %s", after.timeline.selected, oldest)
	}
	// And it is on screen, not merely selected.
	from, to, ok := after.selectionRows()
	if !ok {
		t.Fatal("no selection after home")
	}
	if rows := after.msgAreaRows(); to <= after.timeline.scroll || from >= after.timeline.scroll+rows {
		t.Errorf("the first message is not in the viewport: rows [%d,%d), viewport [%d,%d)",
			from, to, after.timeline.scroll, after.timeline.scroll+rows)
	}
}

// end is the mirror: the newest message, selected and in view.
func TestEndSelectsTheNewestMessage(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.hist.atStart = true
	m.timeline.selected = m.timeline.messages[0].ID
	m.timeline.scroll = 300

	next, _ := asModel(m.Update(tea.KeyPressMsg{Code: tea.KeyEnd}))
	after := next

	shown := after.shownMessages()
	if want := shown[len(shown)-1].ID; after.timeline.selected != want {
		t.Errorf("end selected %s, want the newest message %s", after.timeline.selected, want)
	}
	if after.timeline.scroll != 0 {
		t.Errorf("end left scroll at %d, want the bottom", after.timeline.scroll)
	}
}

// ctrl+u and ctrl+d move half a screen, and the half is the point: what a half page
// buys over a whole one is overlap — some of what you just read stays on screen to
// place the rest against.
func TestHalfPageMovesHalfAScreen(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, false
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
	m.timeline.scroll = 0

	rows := m.msgAreaRows()
	if rows < 4 {
		t.Fatalf("test model too short to tell a half page from a whole one: %d rows", rows)
	}
	up, _ := asModel(m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}))
	half := up
	if want := rows / 2; half.timeline.scroll != want {
		t.Errorf("ctrl+u scrolled %d rows, want %d (half of %d)", half.timeline.scroll, want, rows)
	}
	// The cursor comes with it, for the same reason paging carries it.
	from, to, ok := half.selectionRows()
	if !ok {
		t.Fatal("no selection after ctrl+u")
	}
	if to <= half.timeline.scroll || from >= half.timeline.scroll+rows {
		t.Errorf("ctrl+u left the cursor outside the viewport: rows [%d,%d), viewport [%d,%d)",
			from, to, half.timeline.scroll, half.timeline.scroll+rows)
	}
	// And back down again lands where it started.
	down, _ := asModel(half.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}))
	back := down
	if back.timeline.scroll != 0 {
		t.Errorf("ctrl+d after ctrl+u left the view at %d, want back at the bottom", back.timeline.scroll)
	}
}

// ctrl+d scrolls while typing — the composer's editor has no use for it — where ctrl+u
// is the editor's "clear the line" and must never reach the timeline.
func TestHalfPageWhileTypingIsCtrlDOnly(t *testing.T) {
	m := benchTimelineModel(400)
	m.focus, m.compose.insertMode = paneTimeline, true
	m.timeline.selected = m.timeline.messages[len(m.timeline.messages)-1].ID
	m.timeline.scroll = 40

	typed, _ := asModel(m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}))
	after := typed
	if after.timeline.scroll != 40-max(m.msgAreaRows()/2, 1) {
		t.Errorf("ctrl+d while typing left the view at %d, want %d", after.timeline.scroll, 40-max(m.msgAreaRows()/2, 1))
	}
	if after.timeline.selected != m.timeline.selected {
		t.Error("ctrl+d while typing moved the message cursor, which is not on screen")
	}
}
