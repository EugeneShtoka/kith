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
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!z:x", Name: "Zulu"}}}))

	next, cmd := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = rested(t, next, cmd)
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: msgs}})

	colors := maps.Clone(m.senderColorMap())
	next, cmd = m.selectRoom(domain.Room{ID: mx + "!z:x", Name: "Zulu"})
	m = rested(t, next, cmd)
	return m, colors
}

func TestSenderColorsSurviveARestart(t *testing.T) {
	backend := newSlotStore()
	first := []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob"},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@zara:x", SenderName: "Zara"},
	}
	_, before := colorSession(t, backend, first)
	if len(before) != 2 {
		t.Fatalf("first session saw %d senders, want 2", len(before))
	}
	if len(backend.saved[mx+"!a:x"]) != 2 {
		t.Fatalf("leaving the room saved %v, want both senders", backend.saved[mx+"!a:x"])
	}

	// The second run also loads older history with someone who sorts first.
	second := append([]domain.Message{
		{ID: mx + "$0", RoomID: mx + "!a:x", Sender: "matrix:@aaron:x", SenderName: "Aaron"},
	}, first...)
	_, after := colorSession(t, backend, second)

	for mxid, was := range before {
		if got := after[mxid]; !sameColor(got, was) {
			t.Errorf("%s changed color across the restart: %v → %v", mxid, was, got)
		}
	}
	if _, ok := after["matrix:@aaron:x"]; !ok {
		t.Error("the newcomer was given no color")
	}
	for mxid, c := range before {
		if sameColor(after["matrix:@aaron:x"], c) {
			t.Errorf("the newcomer reused %s's color", mxid)
		}
	}
}

func TestStoredSlotsOverrideALocalGuess(t *testing.T) {
	backend := newSlotStore()
	backend.saved[mx+"!a:x"] = map[string]int{"matrix:@bob:x": 5}

	m := sized(t, update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}}))
	next, cmd := m.selectRoom(domain.Room{ID: mx + "!a:x", Name: "Alpha"})
	m = next
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob"}},
	}})
	// The guess: with only Bob loaded he takes slot 0.
	_ = m.senderColorMap()
	if got := m.derived.slots["matrix:@bob:x"]; got != 0 {
		t.Fatalf("setup: local guess put bob at %d, want 0", got)
	}
	m = rested(t, m, cmd)
	if got := m.derived.slots["matrix:@bob:x"]; got != 5 {
		t.Errorf("bob is at slot %d after the stored answer arrived, want 5", got)
	}
	// The guess is not written back over the stored value.
	if _, pending := m.derived.unsaved["matrix:@bob:x"]; pending {
		t.Error("the local guess is still queued to be written")
	}
}

func TestLateSlotsForAnotherRoomAreDropped(t *testing.T) {
	backend := newSlotStore()
	m := sized(t, update(t, starterNew(backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}}))
	m.openRoom = mx + "!a:x"
	m.derived.slotsRoom = mx + "!a:x"

	after, _ := m.handleSenderSlots(senderSlotsMsg{roomID: mx + "!other:x", slots: map[string]int{"matrix:@ghost:x": 9}})
	if _, leaked := after.derived.slots["matrix:@ghost:x"]; leaked {
		t.Error("another room's slots were adopted")
	}
}
