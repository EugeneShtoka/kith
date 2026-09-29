package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What an assistant may reach is decided from facts the daemon read from the
// homeserver and cached: which rooms are DMs (m.direct), which spaces hold which rooms
// (m.room.create, the space's state), which network a space bridges, a room's name. A
// read that fails, or answers less than it should, must never widen what a scope shares:
// a failure is not "no DMs", "in no space" or "another name".
//
// A seeded world of rooms, DMs and spaces (some a bridge's) is served by a fake
// homeserver; every read can fail. After a healthy refresh the facts must be the world's
// exactly (every entry kind a scope may name is computed). After a refresh in which
// reads failed, no generated scope may allow a room the world's facts would refuse.
// After a healthy refresh again, the facts are the world's again. KITH_SCOPE_SEEDS runs a
// deeper sweep, KITH_SCOPE_SEED one seed.
func TestAFailedReadAtTheSourceNeverWidensTheScope(t *testing.T) {
	t.Parallel()
	first, seeds := 0, 200
	if n, err := strconv.Atoi(os.Getenv("KITH_SCOPE_SEEDS")); err == nil {
		seeds = n
	}
	if one, err := strconv.Atoi(os.Getenv("KITH_SCOPE_SEED")); err == nil {
		first, seeds = one, one+1
	}
	for seed := first; seed < seeds; seed++ {
		if msg := playScopeSeed(t, uint64(seed)); msg != "" {
			t.Fatalf("seed %d: %s", seed, msg)
		}
	}
}

// scopeWorld is what the homeserver holds.
type scopeWorld struct {
	rooms  []string          // room IDs, in order
	names  map[string]string // room ID → name
	direct map[string]bool   // DM rooms (in m.direct)
	// unnamed are DMs with no m.room.name: they are named after the peer's profile.
	unnamed map[string]bool
	spaces  []string            // space IDs
	sname   map[string]string   // space ID → name
	bridge  map[string]string   // space ID → creator (a bridge bot, or me)
	kids    map[string][]string // space ID → child room IDs
}

func newScopeWorld(rng *rand.Rand) scopeWorld {
	w := scopeWorld{
		names: map[string]string{}, direct: map[string]bool{}, unnamed: map[string]bool{}, sname: map[string]string{},
		bridge: map[string]string{}, kids: map[string][]string{},
	}
	for i := range 3 + rng.IntN(4) {
		room := fmt.Sprintf("!r%d:x", i)
		w.rooms = append(w.rooms, room)
		w.names[room] = fmt.Sprintf("Room %d", i)
		w.direct[room] = rng.IntN(3) == 0
		w.unnamed[room] = w.direct[room] && rng.IntN(2) == 0
	}
	for j := range 1 + rng.IntN(3) {
		space := fmt.Sprintf("!s%d:x", j)
		w.spaces = append(w.spaces, space)
		w.sname[space] = fmt.Sprintf("Space %d", j)
		w.bridge[space] = "@me:x"
		if rng.IntN(2) == 0 {
			w.bridge[space] = "@telegrambot:x"
		}
		for _, room := range w.rooms {
			if rng.IntN(2) == 0 {
				w.kids[space] = append(w.kids[space], room)
			}
		}
	}
	return w
}

// facts is the world's own answer for a room: what the cache should say.
func (w scopeWorld) facts(room string) domain.RoomFacts {
	f := domain.RoomFacts{ID: room, Name: w.names[room], Direct: w.direct[room], Protocol: domain.ProtocolMatrix}
	for _, space := range w.spaces {
		if slices.Contains(w.kids[space], room) {
			f.Spaces = append(f.Spaces, w.sname[space])
			if p := domain.ProtocolOf(w.bridge[space]); p.IsBridged() {
				f.Protocol = p
			}
		}
	}
	return f
}

// entries is every entry a scope may name in this world, one of each kind the
// validator accepts for an assistant (pinned is refused there: setup.scopeEntries).
func (w scopeWorld) entries() []string {
	out := []string{"dm", "group", "protocol:telegram", "protocol:matrix"}
	for _, room := range w.rooms {
		out = append(out, room, "room:"+w.names[room])
	}
	for _, space := range w.spaces {
		out = append(out, "space:"+w.sname[space])
	}
	return out
}

// scopeServer serves a world, failing every read whose key is in failing.
type scopeServer struct {
	world   scopeWorld
	mu      sync.Mutex
	failing map[string]bool
}

func (s *scopeServer) fails(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failing[key]
}

func (s *scopeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path, _ := url.PathUnescape(r.URL.Path)
	answer := func(key string, body any) {
		if s.fails(key) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"injected"}`))
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"none"}`))
	}
	world := s.world
	switch {
	case strings.HasSuffix(path, "/joined_rooms"):
		answer("joined", map[string]any{"joined_rooms": append(slices.Clone(world.rooms), world.spaces...)})
	case strings.HasSuffix(path, "/account_data/m.direct"):
		direct := map[string][]string{}
		for i, room := range world.rooms {
			if world.direct[room] {
				peer := fmt.Sprintf("@p%d:x", i)
				direct[peer] = append(direct[peer], room)
			}
		}
		answer("direct", direct)
	case strings.Contains(path, "/profile/@p"):
		// A DM is named after its peer: @p<i> is the peer of room i.
		i, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path[strings.Index(path, "/profile/@p")+len("/profile/@p"):], ""), ":x"))
		room := fmt.Sprintf("!r%d:x", i)
		answer("profile:"+room, map[string]any{"displayname": world.names[room]})
	default:
		room, rest := splitRoomPath(path)
		rest = strings.TrimSuffix(rest, "/")
		switch {
		case room == "":
			notFound()
		case rest == "/state/m.room.create":
			body := map[string]any{"creator": "@me:x"}
			if slices.Contains(world.spaces, room) {
				body["type"] = "m.space"
			}
			answer("create:"+room, body)
		case rest == "/state/m.room.name" && world.unnamed[room]:
			notFound()
		case rest == "/state/m.room.name":
			name, ok := world.names[room]
			if !ok {
				name = world.sname[room]
			}
			answer("name:"+room, map[string]any{"name": name})
		case rest == "/state" && slices.Contains(world.spaces, room):
			answer("state:"+room, spaceState(world, room))
		default:
			notFound()
		}
	}
}

// splitRoomPath splits "/_matrix/client/v3/rooms/!r1:x/state/m.room.name" into the room and
// what follows it.
func splitRoomPath(path string) (string, string) {
	_, after, ok := strings.Cut(path, "/rooms/")
	if !ok {
		return "", ""
	}
	room, rest, _ := strings.Cut(after, "/")
	return room, "/" + rest
}

func spaceState(world scopeWorld, space string) []map[string]any {
	creator := world.bridge[space]
	out := []map[string]any{
		{"type": "m.room.create", "state_key": "", "sender": creator, "event_id": "$c", "room_id": space, "content": map[string]any{"type": "m.space"}},
		{"type": "m.room.name", "state_key": "", "sender": creator, "event_id": "$n", "room_id": space, "content": map[string]any{"name": world.sname[space]}},
	}
	for i, kid := range world.kids[space] {
		out = append(out, map[string]any{
			"type": "m.space.child", "state_key": kid, "sender": creator,
			"event_id": fmt.Sprintf("$k%d", i), "room_id": space, "content": map[string]any{"via": []string{"x"}},
		})
	}
	return out
}

// playScopeSeed is one world: healthy, faulted, healthy again. It returns what broke.
func playScopeSeed(t *testing.T, seed uint64) string {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x5c09e))
	world := newScopeWorld(rng)
	srv := &scopeServer{world: world, failing: map[string]bool{}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	client, err := mautrix.NewClient(hs.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b := New(testCache(t))
	b.client = client

	refresh := func() string {
		if _, err := b.RefreshSpaces(ctx); err != nil {
			return fmt.Sprintf("refresh spaces: %v", err)
		}
		if _, err := b.RefreshRooms(ctx); err != nil {
			return fmt.Sprintf("refresh rooms: %v", err)
		}
		return ""
	}
	exact := func(when string) string {
		for _, room := range world.rooms {
			got, err := b.roomFacts(ctx, domain.RoomID(room))
			if err != nil {
				return fmt.Sprintf("%s: facts of %s: %v", when, room, err)
			}
			if want := world.facts(room); !sameFacts(got, want) {
				return fmt.Sprintf("%s: %s is %+v, the world says %+v", when, room, got, want)
			}
		}
		return ""
	}
	if msg := refresh(); msg != "" {
		return msg
	}
	if msg := exact("healthy"); msg != "" {
		return msg
	}

	// Some reads fail; what is shared may only narrow.
	keys := []string{"direct"}
	for _, room := range append(slices.Clone(world.rooms), world.spaces...) {
		keys = append(keys, "create:"+room, "name:"+room, "profile:"+room)
	}
	for _, space := range world.spaces {
		keys = append(keys, "state:"+space)
	}
	srv.mu.Lock()
	var failed []string
	for _, key := range keys {
		if rng.IntN(3) == 0 {
			srv.failing[key] = true
			failed = append(failed, key)
		}
	}
	srv.mu.Unlock()
	_ = refresh() // a refresh may refuse; the cache must stay safe either way
	entries := world.entries()
	for range 40 {
		scope := domain.ModelScope{
			Only: pick(rng, entries, rng.IntN(3)), Except: pick(rng, entries, 1+rng.IntN(2)),
			Listed: rng.IntN(2) == 0, Encrypted: true,
		}
		for _, room := range world.rooms {
			got, err := b.roomFacts(ctx, domain.RoomID(room))
			if err != nil {
				continue // refused: narrower than anything
			}
			faulted := domain.AllowModel(scope, got, "x", false).Allowed
			truth := domain.AllowModel(scope, world.facts(room), "x", false).Allowed
			if faulted && !truth {
				return fmt.Sprintf("with %v failing, %+v shares %s: its facts read %+v, the world's are %+v",
					failed, scope, room, got, world.facts(room))
			}
		}
	}

	srv.mu.Lock()
	srv.failing = map[string]bool{}
	srv.mu.Unlock()
	if msg := refresh(); msg != "" {
		return msg
	}
	return exact(fmt.Sprintf("healthy again after %v failed", failed))
}

func pick(rng *rand.Rand, from []string, n int) []string {
	var out []string
	for range n {
		out = append(out, from[rng.IntN(len(from))])
	}
	return out
}

func sameFacts(a, b domain.RoomFacts) bool {
	as, bs := slices.Clone(a.Spaces), slices.Clone(b.Spaces)
	slices.Sort(as)
	slices.Sort(bs)
	return a.ID == b.ID && a.Name == b.Name && a.Direct == b.Direct && a.Protocol == b.Protocol &&
		slices.Equal(as, bs)
}
