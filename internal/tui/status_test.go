package tui

import (
	"testing"
	"time"
)

// An event covers the standing line until it expires; the deadline is checked on
// read, and the sweep clears it without re-arming.
func TestEventExpiresBackToTheStandingLine(t *testing.T) {
	t.Parallel()

	m := newModel().doing("38 message(s) · start of room")
	if got := m.status(); got != "38 message(s) · start of room" {
		t.Fatalf("status = %q, want the standing line", got)
	}

	m = m.say("nothing to copy")
	if got := m.status(); got != "nothing to copy" {
		t.Errorf("status = %q, want the event while it is news", got)
	}
	if kept, _ := m.handleStatusTick(); kept.status() != "nothing to copy" {
		t.Errorf("status = %q, want a live event kept by the sweep", kept.status())
	}

	m.st.until = time.Now().Add(-time.Second)
	if got := m.status(); got != "38 message(s) · start of room" {
		t.Errorf("status = %q, want the standing line back", got)
	}

	m.sweepArmed = true
	swept, cmd := m.handleStatusTick()
	if swept.st.event != "" {
		t.Errorf("event = %q, want it swept", swept.st.event)
	}
	if cmd != nil {
		t.Error("the sweep re-armed; an idle client must schedule nothing")
	}
	if swept.sweepArmed {
		t.Error("sweepArmed survived the sweep, so the next event could never arm one")
	}
}

// Saying something arms exactly one sweep; nothing to say, or an already expired
// event, arms none. Asserted on sweepArmed because TestMain stubs the scheduler.
func TestSayingSomethingArmsExactlyOneSweep(t *testing.T) {
	t.Parallel()

	m, cmd := newModel().armStatusSweep()
	if cmd != nil || m.sweepArmed {
		t.Error("an idle client with nothing to say armed a sweep")
	}

	m, _ = m.say("marked Alpha read").armStatusSweep()
	if !m.sweepArmed {
		t.Fatal("saying something armed no sweep, so it would never be cleared")
	}
	if _, cmd = m.armStatusSweep(); cmd != nil {
		t.Error("a second message armed a second timer for the same event")
	}

	stale := newModel().say("stale")
	stale.st.until = time.Now().Add(-time.Second)
	if stale, cmd = stale.armStatusSweep(); cmd != nil || stale.sweepArmed {
		t.Error("an already-expired event armed a sweep")
	}
}

// A new standing line retires the last event.
func TestStandingLineClearsTheEvent(t *testing.T) {
	t.Parallel()

	m := newModel().say("nothing to copy").doing("loading history…")
	if got := m.status(); got != "loading history…" {
		t.Errorf("status = %q, want the new state", got)
	}
	if m.st.event != "" {
		t.Errorf("event = %q, want it dropped", m.st.event)
	}
}
