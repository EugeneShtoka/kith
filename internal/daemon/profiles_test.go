package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// profileWorld is generated rooms, spaces and invitations. Every narrowing call answers
// one item per room it holds, and every query records the rooms it was asked to read.
type profileWorld struct {
	apitest.Nop

	mu      sync.Mutex
	rooms   []domain.Room
	spaces  []domain.Space
	invites []domain.Room
	asked   map[string]domain.RoomSet
}

func (w *profileWorld) set(rooms []domain.Room, spaces []domain.Space, invites []domain.Room) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rooms, w.spaces, w.invites, w.asked = rooms, spaces, invites, map[string]domain.RoomSet{}
}

func (w *profileWorld) held() []domain.Room {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.rooms)
}

// each is one item per room.
func each[T any](w *profileWorld, item func(domain.RoomID) T) []T {
	var out []T
	for _, id := range idsOf(w.held()) {
		out = append(out, item(id))
	}
	return out
}

// read records the room set a query was handed and answers its rooms.
func (w *profileWorld) read(call string, set domain.RoomSet) []domain.RoomID {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked[call] = set
	if !set.All {
		return set.IDs
	}
	return idsOf(w.rooms)
}

func (w *profileWorld) Rooms(context.Context) ([]domain.Room, error)        { return w.held(), nil }
func (w *profileWorld) RefreshRooms(context.Context) ([]domain.Room, error) { return w.held(), nil }

func (w *profileWorld) Spaces(context.Context) ([]domain.Space, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.spaces), nil
}

func (w *profileWorld) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	return w.Spaces(ctx)
}

func (w *profileWorld) CachedInvites(context.Context) ([]domain.Room, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.invites), nil
}

func (w *profileWorld) CachedUnread(context.Context) ([]domain.Unread, error) {
	return each(w, func(id domain.RoomID) domain.Unread { return domain.Unread{RoomID: id, Messages: 1} }), nil
}

func (w *profileWorld) TagVerdicts(context.Context) ([]domain.TagVerdict, error) {
	return each(w, func(id domain.RoomID) domain.TagVerdict { return domain.TagVerdict{Room: id, Tag: "Moved"} }), nil
}

func (w *profileWorld) Drafts(context.Context) ([]domain.StoredDraft, error) {
	return each(w, func(id domain.RoomID) domain.StoredDraft { return domain.StoredDraft{RoomID: id, Body: "x"} }), nil
}

func (w *profileWorld) LastMessages(context.Context) (map[domain.RoomID]time.Time, error) {
	out := map[domain.RoomID]time.Time{}
	for _, id := range idsOf(w.held()) {
		out[id] = time.UnixMilli(1)
	}
	return out, nil
}

func (w *profileWorld) SearchMessages(_ context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	var hits []domain.SearchHit
	for _, id := range w.read("SearchMessages", req.Rooms) {
		hits = append(hits, domain.SearchHit{RoomID: id})
	}
	return hits, nil
}

func (w *profileWorld) SearchSenders(_ context.Context, rooms domain.RoomSet, _ int) ([]domain.Member, error) {
	w.read("SearchSenders", rooms)
	return nil, nil
}

func (w *profileWorld) RoomsWith(_ context.Context, _ []string, rooms domain.RoomSet, _ int) ([]domain.Room, error) {
	var out []domain.Room
	for _, id := range w.read("RoomsWith", rooms) {
		out = append(out, domain.Room{ID: id})
	}
	return out, nil
}

func (w *profileWorld) MessagesFrom(_ context.Context, req domain.SaidRequest) ([]domain.Message, error) {
	var out []domain.Message
	for _, id := range w.read("MessagesFrom", req.Rooms) {
		out = append(out, domain.Message{RoomID: id})
	}
	return out, nil
}

func (w *profileWorld) queried(call string) domain.RoomSet {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.asked[call]
}

// generated is one world and the rooms a profile over it holds, judged by its own
// small oracle over what the world was built from.
type generated struct {
	rooms   []domain.Room
	spaces  []domain.Space
	invites []domain.Room
	profile config.Profile
	tags    []config.Tag
	// in and spacesIn are what the profile holds.
	in, spacesIn map[domain.RoomID]bool
}

// generate builds rooms on two networks, spaces holding rooms and other spaces (cycles
// included), invitations no space knows of, and a profile from entries naming them.
func generate(rng *rand.Rand) generated {
	g := generated{tags: []config.Tag{{Name: "T", Rule: []string{"space:S0"}}}}
	for i := range 2 + rng.IntN(6) {
		g.rooms = append(g.rooms, domain.Room{ID: someRoom(rng, "r", i), Name: fmt.Sprintf("R%d", i), IsDirect: rng.IntN(2) == 0})
	}
	for i := range rng.IntN(3) {
		g.invites = append(g.invites, domain.Room{ID: someRoom(rng, "i", i), Name: fmt.Sprintf("I%d", i), IsDirect: rng.IntN(2) == 0})
	}
	nSpaces := rng.IntN(4)
	for i := range nSpaces {
		g.spaces = append(g.spaces, domain.Space{ID: domain.SpaceID(fmt.Sprintf(mx+"!s%d:x", i)), Name: fmt.Sprintf("S%d", i)})
	}
	for i := range g.spaces {
		for _, id := range idsOf(g.rooms) {
			if rng.IntN(3) == 0 {
				g.spaces[i].Children = append(g.spaces[i].Children, id)
			}
		}
		if nSpaces > 1 && rng.IntN(2) == 0 {
			g.spaces[i].Children = append(g.spaces[i].Children, domain.RoomID(g.spaces[rng.IntN(nSpaces)].ID))
		}
	}
	pool := []string{"dm", "group", "space:S0", "space:S1", "space:S2", "place:S1", "protocol:telegram", "protocol:matrix", "tag:T", "place:T"}
	named := slices.Concat(g.rooms, g.invites)
	for i := range named {
		pool = append(pool, string(named[i].ID), "room:"+strings.ToLower(named[i].Name))
	}
	pick := func() []string {
		var out []string
		for range rng.IntN(3) {
			out = append(out, pool[rng.IntN(len(pool))])
		}
		return out
	}
	g.profile = config.Profile{Name: "p", Include: pick(), Exclude: pick()}
	g.in, g.spacesIn = map[domain.RoomID]bool{}, map[domain.RoomID]bool{}
	for i := range g.rooms {
		g.in[g.rooms[i].ID] = g.holds(&g.rooms[i], true)
	}
	for i := range g.invites {
		g.in[g.invites[i].ID] = g.holds(&g.invites[i], false)
	}
	// A profile holding every room narrows nothing: an empty space stays, as without one.
	every := len(g.profile.Include) == 0 && len(g.profile.Exclude) == 0
	for _, s := range g.spaces {
		g.spacesIn[domain.RoomID(s.ID)] = every || g.reaches(domain.RoomID(s.ID), map[domain.RoomID]bool{})
	}
	return g
}

func someRoom(rng *rand.Rand, kind string, i int) domain.RoomID {
	if rng.IntN(3) == 0 {
		return domain.RoomID(fmt.Sprintf("telegram:1/-100%d%s", i, map[string]string{"r": "1", "i": "2"}[kind]))
	}
	return domain.RoomID(fmt.Sprintf(mx+"!%s%d:x", kind, i))
}

// holds is the oracle: whether the profile holds room. placed says kithd knows the
// room's spaces (an invitation's it does not).
func (g generated) holds(room *domain.Room, placed bool) bool {
	inSpace := func(name string) bool {
		if !placed {
			return false
		}
		for _, s := range g.spaces {
			if strings.EqualFold(s.Name, name) && slices.Contains(s.Children, room.ID) {
				return true
			}
		}
		return false
	}
	names := func(entry string) bool {
		switch {
		case entry == "dm":
			return room.IsDirect
		case entry == "group":
			return !room.IsDirect
		case entry == "tag:T" || entry == "place:T":
			return inSpace("S0")
		case strings.HasPrefix(entry, "space:"):
			return inSpace(strings.TrimPrefix(entry, "space:"))
		case strings.HasPrefix(entry, "place:"):
			return inSpace(strings.TrimPrefix(entry, "place:"))
		case strings.HasPrefix(entry, "protocol:"):
			return strings.HasPrefix(string(room.ID), strings.TrimPrefix(entry, "protocol:")+":")
		case strings.HasPrefix(entry, "room:"):
			return strings.EqualFold(strings.TrimPrefix(entry, "room:"), room.Name)
		}
		return entry == string(room.ID)
	}
	if slices.ContainsFunc(g.profile.Exclude, names) {
		return false
	}
	return len(g.profile.Include) == 0 || slices.ContainsFunc(g.profile.Include, names)
}

// reaches is whether a held room can be reached from a space through its children.
func (g generated) reaches(id domain.RoomID, seen map[domain.RoomID]bool) bool {
	if seen[id] {
		return false
	}
	seen[id] = true
	for _, s := range g.spaces {
		if domain.RoomID(s.ID) != id {
			continue
		}
		return slices.ContainsFunc(s.Children, func(c domain.RoomID) bool { return g.in[c] || g.reaches(c, seen) })
	}
	return false
}

// held is the generated rooms the profile holds, of those listed.
func (g generated) held(rooms []domain.Room) []domain.RoomID {
	var out []domain.RoomID
	for _, id := range idsOf(rooms) {
		if g.in[id] {
			out = append(out, id)
		}
	}
	return out
}

// roomsOf is the rooms a reply names, sorted.
func roomsOf[T any](items []T, room func(T) domain.RoomID) []domain.RoomID {
	out := make([]domain.RoomID, 0, len(items))
	for _, item := range items {
		out = append(out, room(item))
	}
	slices.Sort(out)
	return out
}

// narrowingProbes call each narrowing call and say which rooms reached the client, or
// which rooms the backend was asked to read. A narrowing call without one fails the
// test, so a new one gets checked.
var narrowingProbes = map[string]func(ctx context.Context, r *daemon.Remote, w *profileWorld, g generated) ([]domain.RoomID, error){
	"Rooms": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		rooms, err := r.Rooms(ctx)
		return roomsOf(rooms, roomOf), err
	},
	"RefreshRooms": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		rooms, err := r.RefreshRooms(ctx)
		return roomsOf(rooms, roomOf), err
	},
	"CachedInvites": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		rooms, err := r.CachedInvites(ctx)
		return roomsOf(rooms, roomOf), err
	},
	"CachedUnread": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		unread, err := r.CachedUnread(ctx)
		return roomsOf(unread, func(u domain.Unread) domain.RoomID { return u.RoomID }), err
	},
	"TagVerdicts": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		verdicts, err := r.TagVerdicts(ctx)
		return roomsOf(verdicts, func(v domain.TagVerdict) domain.RoomID { return v.Room }), err
	},
	"Drafts": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		drafts, err := r.Drafts(ctx)
		return roomsOf(drafts, func(d domain.StoredDraft) domain.RoomID { return d.RoomID }), err
	},
	"LastMessages": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, _ generated) ([]domain.RoomID, error) {
		latest, err := r.LastMessages(ctx)
		return slices.Sorted(maps.Keys(latest)), err
	},
	"ScheduledMessages": func(ctx context.Context, r *daemon.Remote, w *profileWorld, _ generated) ([]domain.RoomID, error) {
		for _, m := range mustList(r.ScheduledMessages(ctx)) {
			if err := r.CancelScheduled(ctx, m.ID); err != nil {
				return nil, err
			}
		}
		for _, room := range w.held() {
			if _, err := r.Schedule(ctx, domain.ScheduledMessage{RoomID: room.ID, Body: "later", At: time.Now().Add(time.Hour)}); err != nil {
				return nil, err
			}
		}
		queued, err := r.ScheduledMessages(ctx)
		return roomsOf(queued, func(m domain.ScheduledMessage) domain.RoomID { return m.RoomID }), err
	},
	"SearchMessages": func(ctx context.Context, r *daemon.Remote, w *profileWorld, _ generated) ([]domain.RoomID, error) {
		hits, err := r.SearchMessages(ctx, domain.SearchRequest{Rooms: domain.EveryRoom()})
		return roomsOf(hits, func(h domain.SearchHit) domain.RoomID { return h.RoomID }), err
	},
	"SearchSenders": func(ctx context.Context, r *daemon.Remote, w *profileWorld, _ generated) ([]domain.RoomID, error) {
		_, err := r.SearchSenders(ctx, domain.EveryRoom(), 10)
		return askedRooms(w, "SearchSenders"), err
	},
	"RoomsWith": func(ctx context.Context, r *daemon.Remote, w *profileWorld, _ generated) ([]domain.RoomID, error) {
		// Named rooms, not every room: the list keeps only the profile's.
		var named []domain.RoomID
		for _, room := range w.held() {
			named = append(named, room.ID)
		}
		rooms, err := r.RoomsWith(ctx, []string{alice}, domain.TheseRooms(named), 10)
		return roomsOf(rooms, roomOf), err
	},
	"MessagesFrom": func(ctx context.Context, r *daemon.Remote, w *profileWorld, _ generated) ([]domain.RoomID, error) {
		msgs, err := r.MessagesFrom(ctx, domain.SaidRequest{Senders: []string{alice}, Rooms: domain.EveryRoom()})
		return roomsOf(msgs, func(m domain.Message) domain.RoomID { return m.RoomID }), err
	},
}

func roomOf(r domain.Room) domain.RoomID { return r.ID }

// idsOf is the rooms' IDs, in order.
func idsOf(rooms []domain.Room) []domain.RoomID {
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	return ids
}

func mustList[T any](items []T, _ error) []T { return items }

// askedRooms is the rooms a query was handed, all of them for every room.
func askedRooms(w *profileWorld, call string) []domain.RoomID {
	set := w.queried(call)
	if !set.All {
		return roomsOf(set.IDs, func(id domain.RoomID) domain.RoomID { return id })
	}
	return roomsOf(w.held(), roomOf)
}

// profileServer serves w with a notifier over cfg, which a test reloads.
func profileServer(t *testing.T, w *profileWorld) (*harness, *daemon.Notifications) {
	t.Helper()
	n, err := daemon.NewNotifications(config.Config{}, scopeOnly{w},
		func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatal(err)
	}
	h := serve(t, w, func(d *daemon.Daemon) { d.Notifications = n })
	return h, n
}

// What kithd answers a client running a profile holds only the profile's rooms, and
// every one of them, on every call that narrows and both streams that do; spaces are
// kept while they reach one of its rooms. Worlds, profiles and the reload between them
// are generated; the streams stay open across reloads, so each sees its profile change.
func TestAProfileSeesOnlyItsRooms(t *testing.T) {
	t.Parallel()
	for _, call := range daemon.NarrowingCalls() {
		if _, ok := narrowingProbes[call]; !ok && call != "UnreadStream" && call != "Invites" && call != "Spaces" && call != "RefreshSpaces" {
			t.Errorf("%s narrows to a profile, but nothing here checks it", call)
		}
	}
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	ctx := t.Context()
	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "first"}, {Name: "p"}}}); err != nil {
		t.Fatal(err)
	}
	client := h.client()
	client.UseProfile("p")
	go func() { _ = client.Start(ctx) }()
	defer client.Stop()
	h.waitAttached(1)

	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 7))
		g := generate(rng)
		w.set(g.rooms, g.spaces, g.invites)
		cfg := config.Config{Tags: g.tags, Profiles: []config.Profile{{Name: "first"}, g.profile}}
		if err := n.Reload(cfg); err != nil {
			t.Fatalf("seed %d: reload %+v: %v", seed, g.profile, err)
		}
		n.InvalidateScope()
		what := fmt.Sprintf("seed %d, profile %+v", seed, g.profile)

		want := g.held(g.rooms)
		slices.Sort(want)
		for call, probe := range narrowingProbes {
			expect := want
			if call == "CachedInvites" {
				expect = g.held(g.invites)
				slices.Sort(expect)
			}
			got, err := probe(ctx, client, w, g)
			if err != nil {
				t.Fatalf("%s: %s: %v", what, call, err)
			}
			if !slices.Equal(got, expect) {
				t.Errorf("%s: %s answered %v, want %v", what, call, got, expect)
			}
		}
		checkSpaces(t, what, client, g)
		checkUnreadStream(t, what, client, w, g, want)
		checkInviteStream(t, what, client, w, g)
	}
}

func checkSpaces(t *testing.T, what string, client *daemon.Remote, g generated) {
	t.Helper()
	for _, list := range []func(context.Context) ([]domain.Space, error){client.Spaces, client.RefreshSpaces} {
		spaces, err := list(t.Context())
		if err != nil {
			t.Fatalf("%s: spaces: %v", what, err)
		}
		var got, want []domain.RoomID
		for _, s := range spaces {
			got = append(got, domain.RoomID(s.ID))
			for _, c := range s.Children {
				if !g.in[c] && !g.spacesIn[c] {
					t.Errorf("%s: space %s keeps %s, which the profile does not reach", what, s.Name, c)
				}
			}
		}
		for id, in := range g.spacesIn {
			if in {
				want = append(want, id)
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: spaces %v, want %v", what, got, want)
		}
	}
}

// checkUnreadStream pushes one unread state per room and reads what reaches the client.
func checkUnreadStream(t *testing.T, what string, client *daemon.Remote, w *profileWorld, g generated, want []domain.RoomID) {
	t.Helper()
	for _, id := range idsOf(g.rooms) {
		w.Unreads <- domain.Unread{RoomID: id, Messages: 1}
	}
	var got []domain.RoomID
	quiet := time.After(settle)
	for len(got) < len(want) {
		select {
		case u := <-client.Unread():
			got = append(got, u.RoomID)
		case <-quiet:
			t.Fatalf("%s: the unread stream gave %v, want %v", what, got, want)
		}
	}
	// Anything past want is a room the profile does not hold.
	select {
	case u := <-client.Unread():
		got = append(got, u.RoomID)
	case <-time.After(30 * time.Millisecond):
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%s: the unread stream gave %v, want %v", what, got, want)
	}
}

func checkInviteStream(t *testing.T, what string, client *daemon.Remote, w *profileWorld, g generated) {
	t.Helper()
	w.Invs <- slices.Clone(g.invites)
	select {
	case invites := <-client.Invites():
		got, want := roomsOf(invites, roomOf), g.held(g.invites)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: the invite stream gave %v, want %v", what, got, want)
		}
	case <-time.After(settle):
		t.Fatalf("%s: no invite frame", what)
	}
}

// A client naming no profile runs in the first, one naming a profile finds it in any
// case, and one naming a profile the config lacks is refused on every call, narrowing or
// not. A room named directly is answered outside the profile.
func TestAProfileIsTheFirstOrOneTheConfigHas(t *testing.T) {
	t.Parallel()
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	dm, group := domain.RoomID(mx+"!a:x"), domain.RoomID(mx+"!b:x")
	w.set([]domain.Room{{ID: dm, IsDirect: true}, {ID: group}}, nil, nil)
	cfg := config.Config{Profiles: []config.Profile{{Name: "direct", Include: []string{"dm"}}, {Name: "groups", Include: []string{"group"}}}}
	if err := n.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for name, want := range map[string][]domain.RoomID{"": {dm}, "GROUPS": {group}} {
		client := h.client()
		client.UseProfile(name)
		rooms, err := client.Rooms(ctx)
		if got := roomsOf(rooms, roomOf); err != nil || !slices.Equal(got, want) {
			t.Errorf("profile %q: Rooms = %v, %v; want %v", name, got, err, want)
		}
		if _, err := client.CachedTimeline(ctx, group); err != nil {
			t.Errorf("profile %q: a room named directly was refused: %v", name, err)
		}
	}
	client := h.client()
	client.UseProfile("work")
	if _, err := client.Rooms(ctx); !errors.Is(err, api.ErrUnknownProfile) {
		t.Errorf("an unknown profile: Rooms = %v, want ErrUnknownProfile", err)
	}
	if _, err := client.CachedTimeline(ctx, dm); !errors.Is(err, api.ErrUnknownProfile) {
		t.Errorf("an unknown profile: CachedTimeline = %v, want ErrUnknownProfile", err)
	}
}

// A profile a reload takes away holds nothing for a stream already open in it, and its
// next call is refused: never the first profile instead.
func TestAProfileTakenAwayHoldsNothing(t *testing.T) {
	t.Parallel()
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	room := domain.RoomID(mx + "!a:x")
	w.set([]domain.Room{{ID: room}}, nil, nil)
	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "all"}, {Name: "gone"}}}); err != nil {
		t.Fatal(err)
	}
	client := h.client()
	client.UseProfile("gone")
	go func() { _ = client.Start(t.Context()) }()
	defer client.Stop()
	h.waitAttached(1)
	w.Unreads <- domain.Unread{RoomID: room, Messages: 1}
	if got := take(t, client.Unread(), "Unread()"); got.RoomID != room {
		t.Fatalf("Unread() = %+v before the reload", got)
	}

	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "all"}}}); err != nil {
		t.Fatal(err)
	}
	w.Unreads <- domain.Unread{RoomID: room, Messages: 2}
	select {
	case u := <-client.Unread():
		t.Errorf("a stream in a profile taken away gave %+v", u)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := client.Rooms(t.Context()); !errors.Is(err, api.ErrUnknownProfile) {
		t.Errorf("Rooms in a profile taken away = %v, want ErrUnknownProfile", err)
	}
}
