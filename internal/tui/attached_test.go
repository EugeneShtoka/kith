package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Losing the daemon shows on screen until it is back.
func TestLosingTheDaemonIsOnScreen(t *testing.T) {
	t.Parallel()

	m := attachable(t)
	if badge := m.connectionBadge(); badge != "" {
		t.Fatalf("connectionBadge() = %q while attached, want empty", badge)
	}

	lost := update(t, m, attachedMsg{attached: false})
	if !lost.link.detached {
		t.Error("detached is false after losing the daemon")
	}
	badge := lost.connectionBadge()
	if badge == "" {
		t.Errorf("connectionBadge() = %q, want it to name the lost daemon", badge)
	}
	// It outranks the notification badge in their shared slot.
	if !strings.Contains(stripStyles(lost.renderStatus()), strings.TrimSpace(stripStyles(badge))) {
		t.Errorf("the status bar does not show the lost daemon:\n%s", lost.renderStatus())
	}

	back := update(t, lost, attachedMsg{attached: true})
	if back.link.detached {
		t.Error("detached is still true after the daemon came back")
	}
	if badge := back.connectionBadge(); badge != "" {
		t.Errorf("connectionBadge() = %q once reattached, want empty", badge)
	}
}

// Coming back asks for no timeline: the message stream replays the gap.
func TestComingBackLeavesTheGapToTheStream(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := attachableWith(t, backend)
	backend.timelines.Store(0)

	_, cmd := m.Update(attachedMsg{attached: true})
	deliver(t, m, cmd)
	if got := backend.timelines.Load(); got != 0 {
		t.Errorf("reattaching asked for %d timelines, want none: the stream resumes", got)
	}
}

// A message stream that could not resume has the open room read again, so the gap
// does not stay on screen.
func TestAStreamThatCannotResumeReReadsTheOpenRoom(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := attachableWith(t, backend)
	backend.timelines.Store(0)

	_, cmd := m.Update(changedMsg{change: domain.ChangeMessages})
	deliver(t, m, cmd)
	if got := backend.timelines.Load(); got == 0 {
		t.Error("a stream that could not resume asked for no timeline, so the gap stays on screen")
	}
}

// Losing it asks for nothing.
func TestLosingTheDaemonAsksForNothing(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := attachableWith(t, backend)
	backend.timelines.Store(0)

	_, cmd := m.Update(attachedMsg{attached: false})
	deliver(t, m, cmd)
	if got := backend.timelines.Load(); got != 0 {
		t.Errorf("losing the daemon asked for %d timelines, want none", got)
	}
}

// attachable is a sized model with a room open.
func attachable(t *testing.T) Model {
	t.Helper()
	return attachableWith(t, &countingBackend{})
}

func attachableWith(t *testing.T, backend *countingBackend) Model {
	t.Helper()

	// A closed channel: how a backend says "this will not happen".
	settled := make(chan bool)
	close(settled)
	backend.Attach = settled
	m := update(t, sized(t, starterNew(backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m.openRoom = mx + "!a:x"
	return m.clearStatus()
}
