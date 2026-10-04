package daemon

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
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
	x := newScopeIndex(src, nil, domain.HomeOrder{})
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
	x := newScopeIndex(&gatedRooms{rooms: []domain.Room{{ID: listed, Name: "Choir"}, {ID: "!a:x"}}}, nil, domain.HomeOrder{})
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
// spaces in your order, the first bridge holding it, and the tags holding it.
func TestTheNotifierReadsRoomsAsEveryScopeDoes(t *testing.T) {
	t.Parallel()
	src := &spaced{gatedRooms: gatedRooms{rooms: []domain.Room{{ID: "!r:x", Name: "Standup"}}}, spaces: []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!r:x"}},
		{ID: "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{"!r:x"}},
	}}
	x := newScopeIndex(src, []config.DisplayName{{Target: "!r:x", Name: "Daily"}}, domain.HomeOrder{Priority: []string{"Telegram"}})
	tags, _, err := domain.NewTagSet([]domain.Tag{{Name: "Pinned", Picked: []string{"room:Daily"}}})
	if err != nil {
		t.Fatal(err)
	}
	x.SetTags(tags)
	got := x.Facts(context.Background(), "!r:x")
	if got.Name != "Daily" || got.Protocol != domain.ProtocolWhatsApp || !slices.Equal(got.Tags, []string{"Pinned"}) ||
		len(got.Spaces) != 2 || got.Spaces[0] != "Telegram" {
		t.Errorf("facts = %+v, want Daily, WhatsApp (the first bridge), tag Pinned, Telegram first", got)
	}
}

// The notifier knows a room's tags: a rule can name tag:<name>, and {space} is the
// first home by priority, a tag reading by its name. A reload brings the config's
// tags and priority.
func TestTheNotifierKnowsTags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &spaced{gatedRooms: gatedRooms{rooms: []domain.Room{{ID: "!mom:x", Name: "Mom", IsDirect: true}}},
		spaces: []domain.Space{{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!mom:x"}}}}
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Priority = []string{"tag:Family", "Work"}
	n, err := NewNotifications(cfg, src, "@me:x", func(config.Notifications) notify.Notifier { return nil })
	if err != nil {
		t.Fatal(err)
	}
	facts := n.scope.Facts(ctx, "!mom:x")
	if !facts.Names("tag:Family") {
		t.Fatalf("facts = %+v, want the room's tag known", facts)
	}
	if got := n.scope.Home(facts); got != "Family" {
		t.Errorf("{space} = %q, want the tag that ranks first, by its name", got)
	}
	cfg.Display.Priority = []string{"Work"}
	if err := n.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := n.scope.Home(n.scope.Facts(ctx, "!mom:x")); got != "Work" {
		t.Errorf("{space} after a reload ranking Work first = %q", got)
	}
}
