package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
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

// heldBackend answers as kith-proxy does while the bridge is out of reach.
type heldBackend struct {
	apitest.Nop
	since time.Time
}

func (b heldBackend) HeldSince(context.Context) (time.Time, error) { return b.since, nil }

// While kith-proxy answers from its store, the status bar says the window is offline
// and as of when; answers from kithd again, it says nothing.
func TestHeldAnswersAreOnScreen(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 23, 14, 2, 0, 0, time.Local)
	m := attachable(t)
	m.backend = heldBackend{since: at}
	held := update(t, m, m.loadHeldCmd()())
	badge := stripStyles(held.connectionBadge())
	if !strings.Contains(badge, "offline") || !strings.Contains(badge, "Nov 23 14:02") {
		t.Errorf("connectionBadge() held = %q, want it to say offline as of Nov 23 14:02", badge)
	}

	held.backend = heldBackend{}
	live := update(t, held, held.loadHeldCmd()())
	if badge := live.connectionBadge(); badge != "" {
		t.Errorf("connectionBadge() once live = %q, want empty", badge)
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
