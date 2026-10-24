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

// gatedRooms serves a swappable room list and can hold a read mid-flight.
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

// An invalidation during a rebuild's read wins: the older read is not installed.
func TestInvalidateOvertakesARebuildAlreadyReading(t *testing.T) {
	t.Parallel()

	src := &gatedRooms{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Old name"}}}
	x := newScopeIndex(src, nil, domain.HomeOrder{})
	hold, read := make(chan struct{}), make(chan struct{})
	src.hold, src.read = hold, read

	done := make(chan struct{})
	go func() { defer close(done); x.lookup(context.Background(), mx+"!a:x") }()
	<-read
	src.mu.Lock()
	src.rooms = []domain.Room{{ID: mx + "!a:x", Name: "New name"}}
	src.mu.Unlock()
	x.Invalidate()
	close(hold)
	<-done

	facts, ok := x.lookup(context.Background(), mx+"!a:x")
	if !ok || facts.Name != "New name" {
		t.Errorf("lookup = %+v, %v; want the name read after the invalidation", facts, ok)
	}
}

// A room whose ID names its network is on it before the index lists it.
func TestANativeRoomIsOnItsOwnNetwork(t *testing.T) {
	t.Parallel()

	listed := domain.RoomID("whatsapp:44000000001/120363000000000001@g.us")
	unlisted := domain.RoomID("whatsapp:44000000001/1500000002@s.whatsapp.net")
	x := newScopeIndex(&gatedRooms{rooms: []domain.Room{{ID: listed, Name: "Choir"}, {ID: mx + "!a:x"}}}, nil, domain.HomeOrder{})
	for _, c := range []struct {
		room domain.RoomID
		want domain.Protocol
	}{{listed, domain.ProtocolWhatsApp}, {unlisted, domain.ProtocolWhatsApp}, {mx + "!a:x", domain.ProtocolMatrix}} {
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

// The notifier reads a room as every scope does: your name for it, its spaces in your
// order, the first bridge holding it, and its tags.
func TestTheNotifierReadsRoomsAsEveryScopeDoes(t *testing.T) {
	t.Parallel()
	src := &spaced{gatedRooms: gatedRooms{rooms: []domain.Room{{ID: mx + "!r:x", Name: "Standup"}}}, spaces: []domain.Space{
		{ID: mx + "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{mx + "!r:x"}},
		{ID: mx + "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{mx + "!r:x"}},
	}}
	x := newScopeIndex(src, []config.DisplayName{{Target: mx + "!r:x", Name: "Daily"}}, domain.HomeOrder{Priority: []string{"space:Telegram"}, Labels: map[string]string{"telegram": "TG"}})
	tags, _, err := domain.NewTagSet([]domain.Tag{{Name: "Pinned", Picked: []string{"room:Daily"}}})
	if err != nil {
		t.Fatal(err)
	}
	x.SetTags(tags)
	got := x.Facts(context.Background(), mx+"!r:x")
	if got.Name != "Daily" || got.Protocol != domain.ProtocolWhatsApp || !slices.Equal(got.Tags, []string{"Pinned"}) ||
		len(got.Spaces) != 2 || got.Spaces[0] != "Telegram" {
		t.Errorf("facts = %+v, want Daily, WhatsApp (the first bridge), tag Pinned, Telegram first", got)
	}
	// place: reads the rail's rename and the tags, as the client's facts do.
	if !got.Names("place:tg") || !got.Names("place:pinned") || got.Names("place:Daily") {
		t.Errorf("facts = %+v, want place:TG (Telegram renamed) and place:Pinned to name the room", got)
	}
}

// The notifier knows tags: rules name tag:<name>, {space} is the first home by
// priority, and a reload brings new tags and priority.
func TestTheNotifierKnowsTags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &spaced{gatedRooms: gatedRooms{rooms: []domain.Room{{ID: mx + "!mom:x", Name: "Mom", IsDirect: true}}},
		spaces: []domain.Space{{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{mx + "!mom:x"}}}}
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Priority = []string{"tag:Family", "space:Work"}
	n, err := NewNotifications(cfg, src, func(config.Notifications) notify.Notifier { return nil })
	if err != nil {
		t.Fatal(err)
	}
	facts := n.scope.Facts(ctx, mx+"!mom:x")
	if !facts.Names("tag:Family") {
		t.Fatalf("facts = %+v, want the room's tag known", facts)
	}
	if got := domain.HomeLabel(n.scope.FirstHome(facts)); got != "Family" {
		t.Errorf("{space} = %q, want the tag that ranks first, by its name", got)
	}
	cfg.Display.Priority = []string{"space:Work"}
	if err := n.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := domain.HomeLabel(n.scope.FirstHome(n.scope.Facts(ctx, mx+"!mom:x"))); got != "Work" {
		t.Errorf("{space} after a reload ranking Work first = %q", got)
	}
}

// A network-archived room is in that archive's tag for rules and agent scope; an
// unfollowed network places nothing; a config change needs no reread.
func TestANetworksArchiveIsJudgedAsThePlace(t *testing.T) {
	t.Parallel()
	src := &gatedRooms{rooms: []domain.Room{
		{ID: "telegram:42/7", Name: "Dana", Archived: true},
		{ID: "whatsapp:44/1@g.us", Name: "Group", Archived: true},
	}}
	x := newScopeIndex(src, nil, domain.HomeOrder{})
	tags, _, err := domain.NewTagSet([]domain.Tag{{Name: "Archived"}})
	if err != nil {
		t.Fatal(err)
	}
	x.SetTags(tags)
	x.SetArchives(map[domain.Protocol]string{domain.ProtocolTelegram: "Archived"})
	ctx := context.Background()
	if got := x.Facts(ctx, "telegram:42/7").Tags; !slices.Equal(got, []string{"Archived"}) {
		t.Errorf("Telegram's archived chat is in %v", got)
	}
	if got := x.Facts(ctx, "whatsapp:44/1@g.us").Tags; len(got) != 0 {
		t.Errorf("WhatsApp's archived chat, not followed, is in %v", got)
	}
	x.SetArchives(nil)
	if got := x.Facts(ctx, "telegram:42/7").Tags; len(got) != 0 {
		t.Errorf("after following nothing, Telegram's chat is in %v", got)
	}
}

// Retention follows [storage] by the room's place, read from the index without
// rebuilding (the cache asks mid-write): before indexing, by ID and network alone.
func TestTheCacheKeepsWhatTheRoomsPlaceSays(t *testing.T) {
	t.Parallel()
	small, all := 50, -1
	cfg := config.Config{
		Tags: []config.Tag{{Name: "Archived", Picked: []string{"room:Dana"}}}, // by name: the index knows it
		Storage: config.Storage{MessagesPerRoom: &all, Rules: []config.StorageRule{
			{Match: "tag:Archived", Messages: &small},
		}},
	}
	src := &gatedRooms{rooms: []domain.Room{{ID: "telegram:42/7", Name: "Dana"}, {ID: "telegram:42/8", Name: "Sam"}}}
	n, err := NewNotifications(cfg, src, func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatal(err)
	}
	if got := n.MessagesKept("telegram:42/7"); got != -1 {
		t.Errorf("before the index holds it: keeps %d, want -1 (no rebuild mid-write)", got)
	}
	n.scope.Facts(context.Background(), "telegram:42/7") // build the index, as a notification does
	if got, other := n.MessagesKept("telegram:42/7"), n.MessagesKept("telegram:42/8"); got != 50 || other != -1 {
		t.Errorf("keeps %d in the archived room (want 50) and %d in the other (want -1)", got, other)
	}
}
