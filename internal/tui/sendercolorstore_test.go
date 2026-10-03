package tui

import (
	"context"
	"image/color"
	"maps"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// slotStore remembers color slots like the daemon's cache: a stored slot never moves.
type slotStore struct {
	apitest.Nop
	saved map[domain.RoomID]map[string]int
}

func newSlotStore() *slotStore {
	return &slotStore{saved: map[domain.RoomID]map[string]int{}}
}

func (s *slotStore) SenderSlots(_ context.Context, roomID domain.RoomID) (map[string]int, error) {
	out := map[string]int{}
	maps.Copy(out, s.saved[roomID])
	return out, nil
}

func (s *slotStore) SaveSenderSlots(_ context.Context, roomID domain.RoomID, slots map[string]int) error {
	if s.saved[roomID] == nil {
		s.saved[roomID] = map[string]int{}
	}
	for key, slot := range slots {
		if _, taken := s.saved[roomID][key]; !taken {
			s.saved[roomID][key] = slot
		}
	}
	return nil
}

// colorSession opens a room, loads msgs, reads the colors, then leaves (which saves).
func colorSession(t *testing.T, backend *slotStore, msgs []domain.Message) (Model, map[string]color.Color) {
	t.Helper()
	m := sized(t, update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!z:x", Name: "Zulu"}}}))

	next, cmd := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = rested(t, next, cmd)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: msgs}})

	colors := maps.Clone(m.senderColorMap())
	next, cmd = m.selectRoom(domain.Room{ID: "!z:x", Name: "Zulu"})
	m = rested(t, next, cmd)
	return m, colors
}

// A person keeps their hue across a restart, and a later newcomer renumbers nobody.
func TestSenderColorsSurviveARestart(t *testing.T) {
	backend := newSlotStore()
	first := []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob"},
		{ID: "$2", RoomID: "!a:x", Sender: "@zara:x", SenderName: "Zara"},
	}
	_, before := colorSession(t, backend, first)
	if len(before) != 2 {
		t.Fatalf("first session saw %d senders, want 2", len(before))
	}
	if len(backend.saved["!a:x"]) != 2 {
		t.Fatalf("leaving the room saved %v, want both senders", backend.saved["!a:x"])
	}

	// A second run also loads older history with someone who sorts first.
	second := append([]domain.Message{
		{ID: "$0", RoomID: "!a:x", Sender: "@aaron:x", SenderName: "Aaron"},
	}, first...)
	_, after := colorSession(t, backend, second)

	for mxid, was := range before {
		if got := after[mxid]; !sameColor(got, was) {
			t.Errorf("%s changed color across the restart: %v → %v", mxid, was, got)
		}
	}
	if _, ok := after["@aaron:x"]; !ok {
		t.Error("the newcomer was given no color")
	}
	for mxid, c := range before {
		if sameColor(after["@aaron:x"], c) {
			t.Errorf("the newcomer reused %s's color", mxid)
		}
	}
}

// A stored numbering wins over one this session guessed before the load landed.
func TestStoredSlotsOverrideALocalGuess(t *testing.T) {
	backend := newSlotStore()
	backend.saved["!a:x"] = map[string]int{"@bob:x": 5}

	m := sized(t, update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	next, cmd := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob"}},
	}})
	// The guess: with only Bob loaded he takes slot 0.
	_ = m.senderColorMap()
	if got := m.derived.slots["@bob:x"]; got != 0 {
		t.Fatalf("setup: local guess put bob at %d, want 0", got)
	}
	// The load lands.
	m = rested(t, m, cmd)
	if got := m.derived.slots["@bob:x"]; got != 5 {
		t.Errorf("bob is at slot %d after the stored answer arrived, want 5", got)
	}
	// And the guess is not written back over the stored value.
	if _, pending := m.derived.unsaved["@bob:x"]; pending {
		t.Error("the local guess is still queued to be written")
	}
}

// A late answer for a room already left must not color the room now open.
func TestLateSlotsForAnotherRoomAreDropped(t *testing.T) {
	backend := newSlotStore()
	m := sized(t, update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	m.openRoom = "!a:x"
	m.derived.slotsRoom = "!a:x"

	after, _ := m.handleSenderSlots(senderSlotsMsg{roomID: "!other:x", slots: map[string]int{"@ghost:x": 9}})
	if _, leaked := after.derived.slots["@ghost:x"]; leaked {
		t.Error("another room's slots were adopted")
	}
}
