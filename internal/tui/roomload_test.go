package tui

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// countingBackend counts the room-open fetches.
type countingBackend struct {
	apitest.Nop
	timelines atomic.Int64
	members   atomic.Int64
}

func (b *countingBackend) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	b.timelines.Add(1)
	return nil, nil
}

func (b *countingBackend) MentionCandidates(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	b.members.Add(1)
	return nil, nil
}

func scrolling(t *testing.T, backend *countingBackend) Model {
	t.Helper()
	rooms := make([]domain.Room, 12)
	for i := range rooms {
		rooms[i] = domain.Room{
			ID:   domain.RoomID("!room" + string(rune('a'+i)) + ":x"),
			Name: "Room " + string(rune('A'+i)),
		}
	}
	m := update(t, sized(t, starterNew(backend, config.Display{})), roomsMsg{rooms: rooms})
	m.focus = paneRooms
	return m.clearStatus()
}

// Moving the cursor fetches nothing; a room loads once the cursor rests on it.
func TestScrollingAsksForNothingUntilItSettles(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := scrolling(t, backend)
	backend.timelines.Store(0)
	backend.members.Store(0)

	for range 6 {
		// The returned command (the settle timer) is deliberately not run.
		m, _ = press(t, m, keyText("j"))
	}
	if got := backend.timelines.Load(); got != 0 {
		t.Errorf("six moved rows asked for %d timelines, want none until the cursor rests", got)
	}

	settled := update(t, m, roomLoadMsg{roomID: m.openRoom, armed: m.loadArmed})
	deliver(t, settled, m.loadRoomCmd(m.openRoom))
	if backend.timelines.Load() == 0 {
		t.Error("resting on a room did not load it")
	}
}

// A timer armed for a room the cursor has left is dropped.
func TestAnOutdatedRoomLoadIsDropped(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := scrolling(t, backend)
	first := m.openRoom
	armed := m.loadArmed

	m, _ = press(t, m, keyText("j"))
	if m.openRoom == first {
		t.Fatal("the cursor did not move")
	}
	backend.timelines.Store(0)

	got, cmd := asModel(m.Update(roomLoadMsg{roomID: first, armed: armed}))
	deliver(t, got, cmd)
	if n := backend.timelines.Load(); n != 0 {
		t.Errorf("a stale timer fetched %d timelines, want none", n)
	}
}

// An explicit open loads at once.
func TestOpeningARoomLoadsItAtOnce(t *testing.T) {
	t.Parallel()

	backend := &countingBackend{}
	m := scrolling(t, backend)
	backend.timelines.Store(0)

	next, cmd := press(t, m, keyText("l")) // nav.open
	deliver(t, next, cmd)
	if backend.timelines.Load() == 0 {
		t.Error("opening a room waited for a timer instead of loading it")
	}
}
