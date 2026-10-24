package tui

import (
	"image/color"
	"maps"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestSenderColorsSurviveNewParticipants(t *testing.T) {
	m := newModel()
	m.openRoom = mx + "!a:x"
	m.rooms = m.rooms.withJoined([]domain.Room{{ID: mx + "!a:x", Name: "Room"}})
	m = m.setMessages([]domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x"},
		{ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@zara:x"},
	})
	before := maps.Clone(m.senderColorMap())
	if len(before) != 2 {
		t.Fatalf("senders = %d, want 2", len(before))
	}

	// Someone who sorts before both would shift every index-based slot.
	m = m.setMessages(domain.MergeMessages(m.timeline.messages, []domain.Message{
		{ID: mx + "$0", RoomID: mx + "!a:x", Sender: "matrix:@aaron:x"},
	}))
	after := m.senderColorMap()
	if len(after) != 3 {
		t.Fatalf("senders after the merge = %d, want 3", len(after))
	}
	for mxid, was := range before {
		if got := after[mxid]; !sameColor(got, was) {
			t.Errorf("%s changed color when @aaron:x arrived: %v → %v", mxid, was, got)
		}
	}
	for mxid, c := range before {
		if sameColor(after["matrix:@aaron:x"], c) {
			t.Errorf("@aaron:x reused %s's color %v", mxid, c)
		}
	}
}

func TestSenderColorsResetPerRoom(t *testing.T) {
	m := newModel()
	m.rooms = m.rooms.withJoined([]domain.Room{{ID: mx + "!a:x", Name: "A"}, {ID: mx + "!b:x", Name: "B"}})
	m.openRoom = mx + "!a:x"
	m = m.setMessages([]domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x"}})
	if len(m.senderColorMap()) != 1 {
		t.Fatal("room A should have one sender")
	}
	m.openRoom = mx + "!b:x"
	m = m.setMessages([]domain.Message{{ID: mx + "$2", RoomID: mx + "!b:x", Sender: "matrix:@carol:x"}})
	colors := m.senderColorMap()
	if len(colors) != 1 {
		t.Fatalf("room B senders = %d, want 1", len(colors))
	}
	if _, ok := colors["matrix:@bob:x"]; ok {
		t.Error("room A's sender leaked into room B's colors")
	}
}

func TestDerivedCacheSeesDirectAssignment(t *testing.T) {
	m := newModel()
	m.openRoom = mx + "!a:x"
	m.rooms = m.rooms.withJoined([]domain.Room{{ID: mx + "!a:x", Name: "Room"}})
	m = m.setMessages([]domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@bob:x", SenderName: "Bob"}})
	first := m.nameColWidth()
	m = m.setMessages([]domain.Message{{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@b:x", SenderName: "Bartholomew"}})
	if second := m.nameColWidth(); second == first {
		t.Fatalf("name column stayed %d after the timeline was replaced", second)
	}
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, home, bb, ba := b.RGBA()
	return ar == br && ag == home && ab == bb && aa == ba
}
