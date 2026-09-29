package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Losing the daemon says so, on screen, until it is back.
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
	if badge := lost.connectionBadge(); !strings.Contains(badge, "daemon lost") {
		t.Errorf("connectionBadge() = %q, want it to name the lost daemon", badge)
	}
	// And it outranks the notification badge in the slot they share: "why is nothing
	// happening" has a nearer answer than any do-not-disturb rule.
	if !strings.Contains(lost.renderStatus(), "daemon lost") {
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

// Coming back re-reads the open room.
func TestComingBackReReadsTheOpenRoom(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := attachableWith(t, backend)
	backend.timelines.Store(0)

	_, cmd := m.Update(attachedMsg{attached: true})
	deliver(t, m, cmd)
	if got := backend.timelines.Load(); got == 0 {
		t.Error("reattaching asked for no timeline, so the gap stays on screen")
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

// attachable is a sized model with a room open, which is the state the reconnect has
// something to do in.
func attachable(t *testing.T) Model {
	t.Helper()
	return attachableWith(t, &countingBackend{})
}

func attachableWith(t *testing.T, backend *countingBackend) Model {
	t.Helper()

	// A closed channel, which is how a backend says "this will not happen" — the in-
	// process one answers the same way.
	settled := make(chan bool)
	close(settled)
	backend.Attach = settled
	m := update(t, sized(t, New(context.Background(), backend, config.Display{})),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m.openRoom = "!a:x"
	return m.clearStatus()
}
