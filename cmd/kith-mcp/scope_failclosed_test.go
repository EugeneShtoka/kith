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

// scopeRooms covers every kind of place a scope entry can name: a plain room in a
// space, a bridged room, a room in no space, a DM, and an encrypted room.
func scopeRooms() *fake {
	return &fake{
		rooms: []domain.Room{
			{ID: "!work:x", Name: "Standup"},
			{ID: "!wa:x", Name: "Family"},
			{ID: "!loose:x", Name: "Loose"},
			{ID: "!dm:x", Name: "Dana", IsDirect: true},
			{ID: "!enc:x", Name: "Vault"},
		},
		spaces: []domain.Space{
			{ID: "!s1:x", Name: "Work", Children: []domain.RoomID{"!work:x", "!enc:x"}},
			{ID: "!s2:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!wa:x", "!dm:x"}},
		},
		encrypted: map[domain.RoomID]bool{"!enc:x": true},
	}
}

// scopeEntries is every entry kind, each naming at least one fixture room.
var scopeEntries = []string{
	"space:Work", "space:WhatsApp", "protocol:matrix", "protocol:whatsapp",
	"room:Standup", "!wa:x", "dm", "group",
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
