package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// failing is the fixture with some daemon lookups failing.
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

// Rooms on a directly reached network: in no space, network named by ID (invented
// numbers).
const (
	nativeGroup domain.RoomID = "whatsapp:44000000001/120363000000000001@g.us"
	nativeDM    domain.RoomID = "whatsapp:44000000001/1500000002@s.whatsapp.net"
)

// scopeRooms covers every place kind a scope entry can name: a room in a space, a
// bridged room, a spaceless room, a DM, an encrypted room, and a direct-network group
// and DM.
func scopeRooms() *fake {
	return &fake{
		rooms: []domain.Room{
			{ID: mx + "!work:x", Name: "Standup"},
			{ID: mx + "!wa:x", Name: "Family"},
			{ID: mx + "!loose:x", Name: "Loose"},
			{ID: mx + "!dm:x", Name: "Dana", IsDirect: true},
			{ID: mx + "!enc:x", Name: "Vault"},
			{ID: nativeGroup, Name: "Choir"},
			{ID: nativeDM, Name: "Noa", IsDirect: true},
			{ID: mx + "!both:x", Name: "Relay"}, // held by two bridges
			{ID: mx + "!anon:x"},                // no name of its own
		},
		spaces: []domain.Space{
			{ID: mx + "!s1:x", Name: "Work", Children: []domain.RoomID{mx + "!work:x", mx + "!enc:x"}},
			{ID: mx + "!s2:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{mx + "!wa:x", mx + "!dm:x", mx + "!both:x"}},
			{ID: mx + "!s3:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{mx + "!both:x"}},
		},
		encrypted: map[domain.RoomID]bool{mx + "!enc:x": true},
	}
}

// scopePlaces gives a room your own name, names an unnamed room, shows the WhatsApp
// space as Chats, and adds tags: one picking by your name, one sharing a space's name.
var scopePlaces = domain.Places{
	Names: map[domain.RoomID]string{mx + "!loose:x": "Lounge", mx + "!anon:x": "Quiet"},
	Order: domain.HomeOrder{Priority: []string{"space:Telegram", "space:WhatsApp"}, Labels: map[string]string{"whatsapp": "Chats"}},
	Tags:  pinnedTag(),
}

// pinnedTag picks two rooms (one by your name for it) and takes in a space; Work picks
// a room outside the space of that name.
func pinnedTag() domain.TagSet {
	tags, _, err := domain.NewTagSet([]domain.Tag{{
		Name: "Pinned", Rule: []string{"space:Telegram"}, Picked: []string{"room:Lounge", mx + "!work:x"},
	}, {
		Name: "Work", Picked: []string{mx + "!dm:x"},
	}})
	if err != nil {
		panic(err)
	}
	return tags
}

// scopeEntries is every entry kind, each naming at least one fixture room.
var scopeEntries = []string{
	"space:Work", "space:WhatsApp", "space:Telegram", "protocol:matrix", "protocol:whatsapp",
	"protocol:telegram", "room:Standup", "room:Lounge", "room:Quiet", mx + "!wa:x", "dm", "group",
	"tag:Pinned", string(nativeGroup), "tag:Work",
	"place:Work", "place:chats", "place:Pinned", "place:TELEGRAM", "place:WhatsApp",
}

// strayEntries name no fixture room, though each looks close to one: what the switch to
// matrix:<account>/ IDs could leave in a config (a bare ID, room: with one, another
// account's view of a room, a person). Each must admit nothing.
var strayEntries = []string{
	"!work:x", "room:!work:x", "matrix:fedcba9876543210/!work:x", "room:matrix:fedcba9876543210/!work:x",
	"matrix:@dana:x",
}

func someEntries(rng *rand.Rand) []string {
	var out []string
	for _, e := range append(slices.Clone(scopeEntries), strayEntries...) {
		if rng.IntN(3) == 0 {
			out = append(out, e)
		}
	}
	return out
}

// decisions is what a server decides per fixture room: read, write, and post without
// review.
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

// Whatever the scope and whichever daemon read fails, the rooms read, written or posted
// to are a subset of those allowed when every read works. Random scopes cover mixes;
// every pair of one rooms entry and one except entry covers each kind against each
// fallback (a random mix rarely leaves an except entry the only place-dependent one).
func TestAFailedLookupNeverWidensTheScope(t *testing.T) {
	t.Parallel()
	for seed := range uint64(400) {
		rng := rand.New(rand.NewPCG(seed, 7))
		read := domain.ModelScope{Only: someEntries(rng), Except: someEntries(rng), Encrypted: rng.IntN(2) == 0}
		write := domain.ModelScope{Only: someEntries(rng), Except: someEntries(rng), Encrypted: rng.IntN(2) == 0}
		neverWidens(t, fmt.Sprintf("seed %d", seed), read, write, someEntries(rng))
	}
	for _, only := range append([]string{""}, scopeEntries...) {
		for _, except := range append([]string{""}, scopeEntries...) {
			scope := domain.ModelScope{Only: nonBlank(only), Except: nonBlank(except), Encrypted: true}
			neverWidens(t, fmt.Sprintf("rooms %q, except %q", only, except), scope, scope, nonBlank(only))
		}
	}
}

// A stray entry admits no room; room: with a kith ID admits exactly that room.
func TestAStrayEntryAdmitsNothing(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	for _, entry := range append(slices.Clone(strayEntries), "room:"+mx+"!work:x") {
		s := writerWith(t, f, domain.ModelScope{Only: []string{entry}, Encrypted: true}, domain.ModelScope{}, nil)
		s.places = scopePlaces
		var read []string
		for i := range f.rooms {
			if s.allowed(withCallMemo(context.Background()), f.rooms[i]) == nil {
				read = append(read, f.rooms[i].Name)
			}
		}
		want := []string(nil)
		if strings.HasPrefix(entry, "room:"+mx) {
			want = []string{"Standup"}
		}
		if !slices.Equal(read, want) {
			t.Errorf("rooms = [%s] reads %v, want %v", entry, read, want)
		}
	}
}

// nonBlank is a one-entry list, or none for "".
func nonBlank(entry string) []string {
	if entry == "" {
		return nil
	}
	return []string{entry}
}

// neverWidens fails when some daemon read failing allows a room the healthy run refuses.
func neverWidens(t *testing.T, label string, read, write domain.ModelScope, send []string) {
	t.Helper()
	failures := []failing{{spaces: true}, {encryption: true}, {spaces: true, encryption: true}}
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
		for i := range fixture.rooms {
			room := &fixture.rooms[i]
			for _, c := range []struct {
				what      string
				ok, under bool
			}{
				{"read", healthy.read[room.ID], got.read[room.ID]},
				{"write", healthy.write[room.ID], got.write[room.ID]},
				{"send unreviewed", healthy.send[room.ID], got.send[room.ID]},
			} {
				if c.under && !c.ok {
					t.Fatalf("%s, spaces failing=%v, encryption failing=%v: %s %s is allowed only "+
						"while the lookup fails\nread %+v\nwrite %+v\nsend %v",
						label, fail.spaces, fail.encryption, c.what, room.Name, read, write, send)
				}
			}
		}
	}
}

// Room names and pins admit what they name; a room in two bridged spaces is on the
// first bridge.
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
		{"room:Lounge", mx + "!loose:x", true}, {"room:Loose", mx + "!loose:x", false},
		{"room:Quiet", mx + "!anon:x", true}, {"tag:Pinned", mx + "!loose:x", true}, {"tag:Pinned", mx + "!work:x", true},
		{"tag:Pinned", mx + "!wa:x", false}, {"protocol:whatsapp", mx + "!both:x", true}, {"protocol:telegram", mx + "!both:x", false},
	} {
		s := newServer(f, domain.ModelScope{Only: []string{c.entry}})
		s.places = scopePlaces
		if got := s.allowed(ctx, room(c.room)) == nil; got != c.want {
			t.Errorf("%s admits %s = %v, want %v", c.entry, c.room, got, c.want)
		}
	}
}

// A place: entry admits exactly the rooms of every rail row shown so: a space by its name
// or its rename, a tag, in any case, a space and a tag sharing the name both; as rooms
// or except, for reading and writing alike.
func TestAPlaceEntryAdmitsExactlyTheRoomsOfItsRows(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	ctx := withCallMemo(context.Background())
	all := func(scope domain.ModelScope, write bool) []domain.RoomID {
		s := writerWith(t, f, scope, scope, nil)
		s.places = scopePlaces
		var got []domain.RoomID
		for i := range f.rooms {
			allowed := s.allowed(ctx, f.rooms[i])
			if write {
				allowed = s.writable(ctx, f.rooms[i])
			}
			if allowed == nil {
				got = append(got, f.rooms[i].ID)
			}
		}
		return got
	}
	everyRoom := make([]domain.RoomID, 0, len(f.rooms))
	for i := range f.rooms {
		everyRoom = append(everyRoom, f.rooms[i].ID)
	}
	for _, c := range []struct {
		entry string
		want  []domain.RoomID
	}{
		{"place:Work", []domain.RoomID{mx + "!work:x", mx + "!dm:x", mx + "!enc:x"}},       // the space and the tag
		{"place:chats", []domain.RoomID{mx + "!wa:x", mx + "!dm:x", mx + "!both:x"}},       // WhatsApp, renamed
		{"place:WhatsApp", []domain.RoomID{mx + "!wa:x", mx + "!dm:x", mx + "!both:x"}},    // by its name too
		{"place:TELEGRAM", []domain.RoomID{mx + "!both:x"}},                                // a bridge's space
		{"place:Pinned", []domain.RoomID{mx + "!work:x", mx + "!loose:x", mx + "!both:x"}}, // a tag
		{"place:Lounge", nil}, // a room's name is no row
		{"space:Chats", nil},  // space: reads the name
		{"Work", nil},         // a bare name names nothing
	} {
		for _, write := range []bool{false, true} {
			only := domain.ModelScope{Only: []string{c.entry}, Encrypted: true}
			if got := all(only, write); !slices.Equal(got, c.want) {
				t.Errorf("rooms = [%q] (write %t) admits %v, want %v", c.entry, write, got, c.want)
			}
			except := domain.ModelScope{Only: []string{"dm", "group"}, Except: []string{c.entry}, Encrypted: true}
			want := slices.DeleteFunc(slices.Clone(everyRoom), func(id domain.RoomID) bool { return slices.Contains(c.want, id) })
			if got := all(except, write); !slices.Equal(got, want) {
				t.Errorf("except = [%q] (write %t) admits %v, want %v", c.entry, write, got, want)
			}
		}
	}
}

// A spaceless room is in no space: `except space:Work` keeps it and `protocol:matrix`
// admits it.
func TestARoomInNoSpaceIsAPlainMatrixRoom(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	s := newServer(f, domain.ModelScope{Only: []string{"protocol:matrix"}, Except: []string{"space:Work"}})
	loose := f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == mx+"!loose:x" })]
	if err := s.allowed(withCallMemo(context.Background()), loose); err != nil {
		t.Fatalf("a room in no space was refused: %v", err)
	}
}

// A room whose ID names its network is on it: `protocol:whatsapp` admits it,
// `protocol:matrix` does not.
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

// While spaces are unreadable, every tool says so and asks for a retry instead of
// answering empty.
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
		{"find_rooms_with", map[string]any{"people": []string{"matrix:@dana:x"}}},
		{"read_around", map[string]any{"room": "Loose", "event": mx + "$1"}},
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

// A scope naming no space or network keeps answering while spaces are unreadable.
func TestAScopeNamingNoPlaceIgnoresTheSpaces(t *testing.T) {
	t.Parallel()
	s := newServer(scopeRooms(), domain.ModelScope{Only: []string{"group"}})
	s.backend = failing{fake: scopeRooms(), spaces: true}
	out := call(t, s, "list_rooms", map[string]any{})
	if !listsRoom(out, mx+"!loose:x") {
		t.Fatalf("list_rooms = %v, want the group rooms", out["rooms"])
	}
}

// A tag rule can name a space, so `except = ["tag:Pinned"]` is undecidable while spaces
// are unreadable: the room is refused.
func TestAPinNamingASpaceIsNotGuessed(t *testing.T) {
	t.Parallel()
	f := scopeRooms()
	both := f.rooms[slices.IndexFunc(f.rooms, func(r domain.Room) bool { return r.ID == mx+"!both:x" })]
	s := newServer(f, domain.ModelScope{Only: []string{"room:Relay"}, Except: []string{"tag:Pinned"}})
	s.backend = failing{spaces: true, fake: scopeRooms()}
	s.places = scopePlaces // Pinned takes space:Telegram, which holds !both:x
	if err := s.allowed(withCallMemo(context.Background()), both); err == nil {
		t.Error("a room tagged by its space was shared while the spaces could not be read")
	}
}
