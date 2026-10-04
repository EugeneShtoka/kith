package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// failing is the fixture with some of the daemon's lookups failing.
type failing struct {
	*fake
	spaces, encryption bool
}

var errLookup = errors.New("daemon restarting")

func (f failing) Spaces(ctx context.Context) ([]domain.Space, error) {
	if f.spaces {
		return nil, errLookup
	}
	return f.fake.Spaces(ctx)
}

func (f failing) RoomEncryption(ctx context.Context, rooms []domain.RoomID) (map[domain.RoomID]bool, error) {
	if f.encryption {
		return nil, errLookup
	}
	return f.fake.RoomEncryption(ctx, rooms)
}

// Rooms on a network kith reaches directly: in no space, their network named by their
// IDs alone (invented numbers).
const (
	nativeGroup domain.RoomID = "whatsapp:359000000001/120363000000000001@g.us"
	nativeDM    domain.RoomID = "whatsapp:359000000001/972500000002@s.whatsapp.net"
)

// scopeRooms covers every kind of place a scope entry can name: a plain room in a
// space, a bridged room, a room in no space, a DM, an encrypted room, and a group and
// a DM on a network reached directly.
func scopeRooms() *fake {
	return &fake{
		rooms: []domain.Room{
			{ID: "!work:x", Name: "Standup"},
			{ID: "!wa:x", Name: "Family"},
			{ID: "!loose:x", Name: "Loose"},
			{ID: "!dm:x", Name: "Dana", IsDirect: true},
			{ID: "!enc:x", Name: "Vault"},
			{ID: nativeGroup, Name: "Choir"},
			{ID: nativeDM, Name: "Noa", IsDirect: true},
			{ID: "!both:x", Name: "Relay"}, // held by two bridges
			{ID: "!anon:x"},                // no name of its own
		},
		spaces: []domain.Space{
			{ID: "!s1:x", Name: "Work", Children: []domain.RoomID{"!work:x", "!enc:x"}},
			{ID: "!s2:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!wa:x", "!dm:x", "!both:x"}},
			{ID: "!s3:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{"!both:x"}},
		},
		encrypted: map[domain.RoomID]bool{"!enc:x": true},
	}
}

// scopePlaces are the names, space order and tags the fixture is read with: a room
// called something of your own, an unnamed room named, and a tag picking by that name.
var scopePlaces = domain.Places{
	Names: map[domain.RoomID]string{"!loose:x": "Lounge", "!anon:x": "Quiet"},
	Order: domain.HomeOrder{Priority: []string{"Telegram", "WhatsApp"}},
	Tags:  pinnedTag(),
}

// pinnedTag picks two rooms, one by your name for it, and takes in a space.
func pinnedTag() domain.TagSet {
	tags, _, err := domain.NewTagSet([]domain.Tag{{
		Name: "Pinned", Rule: []string{"space:Telegram"}, Picked: []string{"room:Lounge", "!work:x"},
	}})
	if err != nil {
		panic(err)
	}
	return tags
}

// scopeEntries is every entry kind, each naming at least one fixture room.
var scopeEntries = []string{
	"space:Work", "space:WhatsApp", "space:Telegram", "protocol:matrix", "protocol:whatsapp",
	"protocol:telegram", "room:Standup", "room:Lounge", "room:Quiet", "!wa:x", "dm", "group",
	"tag:Pinned", string(nativeGroup),
}

func someEntries(rng *rand.Rand) []string {
	var out []string
	for _, e := range scopeEntries {
		if rng.IntN(3) == 0 {
			out = append(out, e)
		}
	}
	return out
}

// decisions is what a server decides about every fixture room: may it read it, write
// to it, and post to it without review.
type decisions struct{ read, write, send map[domain.RoomID]bool }

func decide(t *testing.T, s *server, rooms []domain.Room) decisions {
	t.Helper()
	d := decisions{read: map[domain.RoomID]bool{}, write: map[domain.RoomID]bool{}, send: map[domain.RoomID]bool{}}
	for i := range rooms {
		room := rooms[i]
		ctx := withCallMemo(context.Background())
		d.read[room.ID] = s.allowed(ctx, room) == nil
		d.write[room.ID] = s.writable(ctx, room) == nil
		if choice, err := s.howToWrite(ctx, room); err == nil {
			d.send[room.ID] = choice.send
		}
	}
	return d
}

// A failed lookup may only take rooms away: whatever the scope says and whichever of
// the daemon's reads fails, the rooms read, written or posted to then are a subset of
// the rooms allowed when every read works.
func TestAFailedLookupNeverWidensTheScope(t *testing.T) {
	t.Parallel()
	failures := []failing{{spaces: true}, {encryption: true}, {spaces: true, encryption: true}}
	for seed := range uint64(400) {
		rng := rand.New(rand.NewPCG(seed, 7))
		read := domain.ModelScope{Only: someEntries(rng), Except: someEntries(rng), Encrypted: rng.IntN(2) == 0}
		write := domain.ModelScope{Only: someEntries(rng), Except: someEntries(rng), Encrypted: rng.IntN(2) == 0}
		send := someEntries(rng)
		build := func(backend daemonCalls) *server {
			s := writerWith(t, scopeRooms(), read, write, send)
			s.backend = backend
			s.places = scopePlaces
			s.cooldown = 0
			return s
		}
		fixture := scopeRooms()
		healthy := decide(t, build(fixture), fixture.rooms)
		for _, fail := range failures {
			fail.fake = scopeRooms()
			got := decide(t, build(fail), fixture.rooms)
			for _, room := range fixture.rooms {
				for _, c := range []struct {
					what      string
					ok, under bool
				}{
					{"read", healthy.read[room.ID], got.read[room.ID]},
					{"write", healthy.write[room.ID], got.write[room.ID]},
					{"send unreviewed", healthy.send[room.ID], got.send[room.ID]},
				} {
					if c.under && !c.ok {
						t.Fatalf("seed %d, spaces failing=%v, encryption failing=%v: %s %s is allowed only "+
							"while the lookup fails\nread %+v\nwrite %+v\nsend %v",
							seed, fail.spaces, fail.encryption, c.what, room.Name, read, write, send)
					}
				}
			}
		}
	}
}

// A name you gave a room, and a pin, admit the rooms they name — as every other scope
// reads them — and a room in two bridged spaces is on the bridge of the first.
func TestNamesAndPinsAdmitWhatTheyName(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	ctx := withCallMemo(context.Background())
	room := func(id domain.RoomID) domain.Room {
		return f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == id })]
	}
	for _, c := range []struct {
		entry string
		room  domain.RoomID
		want  bool
	}{
		{"room:Lounge", "!loose:x", true}, {"room:Loose", "!loose:x", false},
		{"room:Quiet", "!anon:x", true}, {"tag:Pinned", "!loose:x", true}, {"tag:Pinned", "!work:x", true},
		{"tag:Pinned", "!wa:x", false}, {"protocol:whatsapp", "!both:x", true}, {"protocol:telegram", "!both:x", false},
	} {
		s := newServer(f, domain.ModelScope{Only: []string{c.entry}})
		s.places = scopePlaces
		if got := s.allowed(ctx, room(c.room)) == nil; got != c.want {
			t.Errorf("%s admits %s = %v, want %v", c.entry, c.room, got, c.want)
		}
	}
}

// A room that is in no space is truly in none: with the lookup working, `except
// space:Work` does not exclude it, and `protocol:matrix` admits it.
func TestARoomInNoSpaceIsAPlainMatrixRoom(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	s := newServer(f, domain.ModelScope{Only: []string{"protocol:matrix"}, Except: []string{"space:Work"}})
	loose := f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == "!loose:x" })]
	if err := s.allowed(withCallMemo(context.Background()), loose); err != nil {
		t.Fatalf("a room in no space was refused: %v", err)
	}
}

// A room whose ID names its network is on that network: `protocol:whatsapp` admits it
// and `protocol:matrix` does not, though no bridge space holds it.
func TestANativeRoomIsOnItsOwnNetwork(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	ctx := withCallMemo(context.Background())
	for _, id := range []domain.RoomID{nativeGroup, nativeDM} {
		room := f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == id })]
		if err := newServer(f, domain.ModelScope{Only: []string{"protocol:whatsapp"}}).allowed(ctx, room); err != nil {
			t.Errorf("protocol:whatsapp refused %s: %v", room.Name, err)
		}
		if err := newServer(f, domain.ModelScope{Only: []string{"protocol:matrix"}}).allowed(ctx, room); err == nil {
			t.Errorf("protocol:matrix admitted %s, a WhatsApp chat", room.Name)
		}
	}
}

// While the spaces cannot be read, every tool says so and asks for a retry, rather than
// answering as if the shared rooms were empty — or, before, as if no room were in any
// space.
func TestEveryToolSaysWhenTheSpacesAreUnknown(t *testing.T) {
	t.Parallel()
	scope := domain.ModelScope{Only: []string{"group"}, Except: []string{"space:Work"}}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list_rooms", map[string]any{}},
		{"read_room", map[string]any{"room": "Loose"}},
		{"search_messages", map[string]any{"query": "morning"}},
		{"find_people", map[string]any{"query": "dana"}},
		{"find_rooms_with", map[string]any{"people": []string{"@dana:x"}}},
		{"read_around", map[string]any{"room": "Loose", "event": "$1"}},
		{"unread_summary", map[string]any{}},
		{"send_message", map[string]any{"room": "Loose", "text": "hi"}},
	} {
		t.Run(c.tool, func(t *testing.T) {
			t.Parallel()
			s := writerWith(t, scopeRooms(), scope, domain.ModelScope{}, everything)
			s.backend = failing{fake: scopeRooms(), spaces: true}
			if got := callErr(t, s, c.tool, c.args); !strings.Contains(got, "could not tell which spaces") {
				t.Fatalf("%s said %q, want the spaces named as the reason", c.tool, got)
			}
		})
	}
}

// A scope that names no space or network does not depend on the spaces, so it keeps
// answering while they cannot be read.
func TestAScopeNamingNoPlaceIgnoresTheSpaces(t *testing.T) {
	t.Parallel()
	s := newServer(scopeRooms(), domain.ModelScope{Only: []string{"group"}})
	s.backend = failing{fake: scopeRooms(), spaces: true}
	out := call(t, s, "list_rooms", map[string]any{})
	if !listsRoom(out, "!loose:x") {
		t.Fatalf("list_rooms = %v, want the group rooms", out["rooms"])
	}
}

// A tag's rule can name a space, so `except = ["tag:Pinned"]` cannot be decided while
// the spaces are unreadable: the room is refused, not taken for outside the tag.
func TestAPinNamingASpaceIsNotGuessed(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	both := f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == "!both:x" })]
	s := newServer(f, domain.ModelScope{Only: []string{"room:Relay"}, Except: []string{"tag:Pinned"}})
	s.backend = failing{spaces: true, fake: scopeRooms()}
	s.places = scopePlaces // Pinned takes space:Telegram, which holds !both:x
	if err := s.allowed(withCallMemo(context.Background()), both); err == nil {
		t.Error("a room tagged by its space was shared while the spaces could not be read")
	}
}
