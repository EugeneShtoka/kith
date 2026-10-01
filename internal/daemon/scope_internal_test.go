package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// gatedRooms serves a room list that can be swapped, and can hold a read mid-flight.
type gatedRooms struct {
	mu    sync.Mutex
	rooms []domain.Room
	hold  chan struct{} // when set, the next read waits on it
	read  chan struct{} // signaled once that read has taken its snapshot
}

func (g *gatedRooms) Rooms(context.Context) ([]domain.Room, error) {
	g.mu.Lock()
	rooms, hold, read := g.rooms, g.hold, g.read
	g.hold = nil
	g.mu.Unlock()
	if hold != nil {
		close(read)
		<-hold
	}
	return rooms, nil
}

func (g *gatedRooms) Spaces(context.Context) ([]domain.Space, error) { return nil, nil }

func (g *gatedRooms) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool {
	return false
}

// An invalidation that lands while a rebuild is reading wins: the rebuild's older
// read is not installed, and the next lookup reads again.
func TestInvalidateOvertakesARebuildAlreadyReading(t *testing.T) {
	t.Parallel()

	src := &gatedRooms{rooms: []domain.Room{{ID: "!a:x", Name: "Old name"}}}
	x := newScopeIndex(src, nil, nil)
	hold, read := make(chan struct{}), make(chan struct{})
	src.hold, src.read = hold, read

	done := make(chan struct{})
	go func() { defer close(done); x.lookup(context.Background(), "!a:x") }()
	<-read
	src.mu.Lock()
	src.rooms = []domain.Room{{ID: "!a:x", Name: "New name"}}
	src.mu.Unlock()
	x.Invalidate()
	close(hold)
	<-done

	facts, ok := x.lookup(context.Background(), "!a:x")
	if !ok || facts.Name != "New name" {
		t.Errorf("lookup = %+v, %v; want the name read after the invalidation", facts, ok)
	}
}

// A room whose ID names its network is on that network whether or not the index has
// listed it yet, though no bridge space holds it.
func TestANativeRoomIsOnItsOwnNetwork(t *testing.T) {
	t.Parallel()

	listed := domain.RoomID("whatsapp:359000000001/120363000000000001@g.us")
	unlisted := domain.RoomID("whatsapp:359000000001/972500000002@s.whatsapp.net")
	x := newScopeIndex(&gatedRooms{rooms: []domain.Room{{ID: listed, Name: "Choir"}, {ID: "!a:x"}}}, nil, nil)
	for _, c := range []struct {
		room domain.RoomID
		want domain.Protocol
	}{{listed, domain.ProtocolWhatsApp}, {unlisted, domain.ProtocolWhatsApp}, {"!a:x", domain.ProtocolMatrix}} {
		if got := x.Facts(context.Background(), c.room).Protocol; got != c.want {
			t.Errorf("Facts(%s).Protocol = %q, want %q", c.room, got, c.want)
		}
	}
}

// spaced serves rooms and a space hierarchy.
type spaced struct {
	gatedRooms
	spaces []domain.Space
}

func (s *spaced) Spaces(context.Context) ([]domain.Space, error) { return s.spaces, nil }

// The notifier reads a room as every other scope does: by the name you gave it, its
// spaces in your order, the first bridge holding it, and your pins.
func TestTheNotifierReadsRoomsAsEveryScopeDoes(t *testing.T) {
	t.Parallel()
	src := &spaced{gatedRooms: gatedRooms{rooms: []domain.Room{{ID: "!r:x", Name: "Standup"}}}, spaces: []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!r:x"}},
		{ID: "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{"!r:x"}},
	}}
	x := newScopeIndex(src, []config.DisplayName{{Target: "!r:x", Name: "Daily"}}, []string{"Telegram"})
	x.SetPinned([]string{"room:Daily"})
	got := x.Facts(context.Background(), "!r:x")
	if got.Name != "Daily" || got.Protocol != domain.ProtocolWhatsApp || !got.Pinned ||
		len(got.Spaces) != 2 || got.Spaces[0] != "Telegram" {
		t.Errorf("facts = %+v, want Daily, WhatsApp (the first bridge), pinned, Telegram first", got)
	}
}
