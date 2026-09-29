package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hue slots are persisted state, so Update assigns them and View must not.
func TestViewAssignsNoColors(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	next, _ := asModel(m.Update(cachedTimelineMsg{roomID: "!a:x", messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@ann:x", Body: "one"},
		{ID: "$2", RoomID: "!a:x", Sender: "@bo:x", Body: "two"},
		{ID: "$3", RoomID: "!a:x", Sender: "@cy:x", Body: "three"},
		{ID: "$4", RoomID: "!a:x", Sender: "@di:x", Body: "four"},
	}}))
	m = next
	if m.derived == nil {
		t.Fatal("no derived cache")
	}
	if len(m.derived.slots) == 0 {
		t.Fatal("Update settled no hue slots — the warm is not running, so View is still doing it")
	}

	before, beforeNext := len(m.derived.slots), m.derived.next
	beforeUnsaved := len(m.derived.unsaved)

	for range 3 {
		_ = m.View()
	}

	if got := len(m.derived.slots); got != before {
		t.Errorf("View assigned %d new hue slots (%d → %d)", got-before, before, got)
	}
	if m.derived.next != beforeNext {
		t.Errorf("View advanced the slot counter: %d → %d", beforeNext, m.derived.next)
	}
	if got := len(m.derived.unsaved); got != beforeUnsaved {
		t.Errorf("View queued %d slots for the cache (%d → %d)", got-beforeUnsaved, beforeUnsaved, got)
	}
}
