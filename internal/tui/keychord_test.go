package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An incomplete chord waits and says so; the second step completes it.
func TestChordWaitsThenActs(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Timestamp: at(1)},
		{ID: mx + "$2", RoomID: mx + "!a:x", Timestamp: at(2)},
		{ID: mx + "$3", RoomID: mx + "!a:x", Timestamp: at(3)},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false

	m, _ = press(t, m, keyText("g"))
	if got := m.selectedID(); got != mx+"$3" {
		t.Errorf("selected = %q, want the newest still — half a chord acts on nothing", got)
	}
	if !strings.Contains(m.chordPending(), "g") {
		t.Errorf("pending = %q, want the sequence so far", m.chordPending())
	}

	m, _ = press(t, m, keyText("g"))
	if got := m.selectedID(); got != mx+"$1" {
		t.Errorf("selected = %q, want the oldest after gg", got)
	}
	if m.chordPending() != "" {
		t.Errorf("pending = %q, want the sequence finished", m.chordPending())
	}
}

// A key that cannot continue the sequence ends it and does its own job.
func TestChordAbandonedByAnUnrelatedKey(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus, m.compose.insertMode = paneTimeline, false

	m, _ = press(t, m, keyText("g"))
	m, _ = press(t, m, keyText("i")) // insert, not a continuation
	if m.chordPending() != "" {
		t.Errorf("pending = %q, want the sequence abandoned", m.chordPending())
	}
	if !m.compose.insertMode {
		t.Error("the key that ended the chord should still have done its own job")
	}
}

// While typing, a sequence must never hold a key back.
func TestChordsAreSuspendedWhileTyping(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus, m.compose.insertMode = paneTimeline, true

	m, _ = press(t, m, keyText("g"))
	if m.compose.input != "g" {
		t.Errorf("input = %q, want the letter typed rather than held", m.compose.input)
	}
	if m.chordPending() != "" {
		t.Errorf("pending = %q, want no chord while typing", m.chordPending())
	}
}
