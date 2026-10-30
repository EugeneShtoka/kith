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

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
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
	include := pick()
	if len(include) == 0 || rng.IntN(4) == 0 {
		include = append(include, "*")
	}
	g.profile = config.Profile{Name: "p", Include: include, Exclude: pick()}
	g.in, g.spacesIn = map[domain.RoomID]bool{}, map[domain.RoomID]bool{}
	for i := range g.rooms {
		g.in[g.rooms[i].ID] = g.holds(&g.rooms[i], true)
	}
	for i := range g.invites {
		g.in[g.invites[i].ID] = g.holds(&g.invites[i], false)
	}
	// A profile holding every room narrows nothing: an empty space stays, as without one.
	every := slices.Contains(g.profile.Include, "*") && len(g.profile.Exclude) == 0
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
	return slices.Contains(g.profile.Include, "*") || slices.ContainsFunc(g.profile.Include, names)
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
	// The rail's rooms, joined and invited; a row holding a room, or a space's row for a
	// space, the profile does not reach fails the call.
	"Rail": func(ctx context.Context, r *daemon.Remote, _ *profileWorld, g generated) ([]domain.RoomID, error) {
		rail, err := r.Rail(ctx)
		var rooms []domain.RoomID
		for _, room := range rail.Rooms {
			rooms = append(rooms, domain.RoomID(room.Facts.ID))
		}
		for _, row := range rail.Rows {
			if row.Space != "" && !g.spacesIn[domain.RoomID(row.Space)] {
				return nil, fmt.Errorf("the rail has a row for space %s, which the profile does not reach", row.Space)
			}
			for _, id := range row.Rooms {
				if !slices.Contains(rooms, id) {
					return nil, fmt.Errorf("row %q holds %s, which the rail does not list", row.Key, id)
				}
			}
		}
		slices.Sort(rooms)
		return rooms, err
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
	n, err := daemon.NewNotifications(asStarted(config.Config{}), scopeOnly{w})
	if err != nil {
		t.Fatal(err)
	}
	h := serve(t, w, func(d *daemon.Daemon) { d.Notifications = n })
	return h, n
}

// What kithd answers a client running a profile holds only the profile's rooms, and
// every one of them, on every call that narrows and the streams that do; spaces are
// kept while they reach one of its rooms. Worlds, profiles and the reload between them
// are generated; the streams stay open across reloads, so each sees its profile change.
func TestAProfileSeesOnlyItsRooms(t *testing.T) {
	t.Parallel()
	for _, call := range daemon.NarrowingCalls() {
		if _, ok := narrowingProbes[call]; !ok && call != "UnreadStream" && call != "Invites" && call != "Spaces" && call != "RefreshSpaces" &&
			call != "NotificationStream" {
			t.Errorf("%s narrows to a profile, but nothing here checks it", call)
		}
	}
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	ctx := t.Context()
	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "first", Include: []string{"*"}}, {Name: "p", Include: []string{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	client := h.client()
	client.UseProfile("p")
	go func() { _ = client.Start(ctx) }()
	defer client.Stop()
	h.waitAttached(1)
	notes := listen(t, client)

	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 7))
		g := generate(rng)
		w.set(g.rooms, g.spaces, g.invites)
		cfg := notifsOn("all")
		cfg.Tags, cfg.Profiles = g.tags, []config.Profile{{Name: "first", Include: []string{"*"}}, g.profile}
		if err := n.Reload(cfg); err != nil {
			t.Fatalf("seed %d: reload %+v: %v", seed, g.profile, err)
		}
		n.InvalidateScope()
		what := fmt.Sprintf("seed %d, profile %+v", seed, g.profile)

		want := g.held(g.rooms)
		slices.Sort(want)
		for call, probe := range narrowingProbes {
			expect := want
			switch call {
			case "CachedInvites":
				expect = g.held(g.invites)
				slices.Sort(expect)
			case "Rail":
				expect = slices.Sorted(slices.Values(slices.Concat(g.held(g.rooms), g.held(g.invites))))
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
		checkNotificationStream(t, what, n, notes, g, want)
	}
}

// listen subscribes client to its profile's notifications, once the daemon has.
func listen(t *testing.T, client *daemon.Remote) <-chan daemon.Notice {
	t.Helper()
	notes, opened := make(chan daemon.Notice, 64), make(chan struct{})
	go func() {
		_ = client.Notifications(t.Context(), func() { close(opened) }, func(v daemon.Notice) { notes <- v })
	}()
	select {
	case <-opened:
	case <-time.After(settle):
		t.Fatal("the notification stream never opened")
	}
	return notes
}

// checkNotificationStream has every room notify and reads which reach the profile's
// stream; each message's body is its room.
func checkNotificationStream(t *testing.T, what string, n *daemon.Notifications, notes <-chan daemon.Notice, g generated, want []domain.RoomID) {
	t.Helper()
	for _, id := range idsOf(g.rooms) {
		n.Synced(domain.OwnerOf(id), time.Now())
		n.Synced(domain.OwnerOf(id), time.Now()) // past the catch-up batch
		n.Deliver(t.Context(), msg(id, alice, string(id)))
	}
	var got []domain.RoomID
	quiet := time.After(settle)
	for len(got) < len(want) {
		select {
		case v := <-notes:
			got = append(got, domain.RoomID(v.Alert.Body))
		case <-quiet:
			t.Fatalf("%s: the notification stream gave %v, want %v", what, got, want)
		}
	}
	select {
	case v := <-notes:
		got = append(got, domain.RoomID(v.Alert.Body))
	case <-time.After(30 * time.Millisecond):
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%s: the notification stream gave %v, want %v", what, got, want)
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

// A profile leaving out its archive leaves out what it archives: phone archives Signal,
// so it lists Slack only; one on the shared archive (which holds neither) lists both.
func TestAProfileLeavesOutItsOwnArchive(t *testing.T) {
	t.Parallel()
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	signal, slack := domain.RoomID(mx+"!s:x"), domain.RoomID(mx+"!k:x")
	w.set([]domain.Room{{ID: signal, Network: domain.ProtocolSignal}, {ID: slack, Network: domain.ProtocolSlack}}, nil, nil)
	cfg := config.Config{
		Tags: []config.Tag{{Name: "Archived", Rule: []string{"space:Nowhere"}}},
		Profiles: []config.Profile{
			{Name: "shared", Include: []string{"*"}, Exclude: []string{"tag:Archived"}},
			{
				Name: "phone", Include: []string{"*"}, Exclude: []string{"tag:Archived"},
				Archive: config.ProfileArchive{Rooms: []string{"protocol:signal"}},
			},
		},
	}
	if err := n.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]domain.RoomID{"shared": {slack, signal}, "phone": {slack}} {
		client := h.client()
		client.UseProfile(name)
		rooms, err := client.Rooms(t.Context())
		got := roomsOf(rooms, roomOf)
		slices.Sort(got)
		slices.Sort(want)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("profile %q: Rooms = %v, %v; want %v", name, got, err, want)
		}
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
	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "all", Include: []string{"*"}}, {Name: "gone", Include: []string{"*"}}}}); err != nil {
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

	if err := n.Reload(config.Config{Profiles: []config.Profile{{Name: "all", Include: []string{"*"}}}}); err != nil {
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

// Switching profiles, alone or two at once, with unread arriving meanwhile: once the
// switches return, the streams run in the profile a call names, and nothing the old
// profile streamed comes after. A switch racing Stop returns; one before Start only
// names the profile.
func TestSwitchingProfilesMovesEveryStream(t *testing.T) {
	t.Parallel()
	w := &profileWorld{Nop: nopWithChannels()}
	h, n := profileServer(t, w)
	dm, group, tg := domain.RoomID(mx+"!a:x"), domain.RoomID(mx+"!b:x"), domain.RoomID("telegram:1/-1003")
	w.set([]domain.Room{{ID: dm, IsDirect: true}, {ID: group}, {ID: tg}}, nil, nil)
	profiles := []string{"all", "direct", "groups", "telegram"}
	if err := n.Reload(config.Config{Profiles: []config.Profile{
		{Name: "all", Include: []string{"*"}}, {Name: "direct", Include: []string{"dm"}},
		{Name: "groups", Include: []string{"group"}, Exclude: []string{"protocol:telegram"}},
		{Name: "telegram", Include: []string{"protocol:telegram"}},
	}}); err != nil {
		t.Fatal(err)
	}
	client := h.client()
	_ = client.SwitchProfile("direct") // before Start: names it only
	ctx := t.Context()
	go func() { _ = client.Start(ctx) }()
	defer client.Stop()
	h.waitAttached(1)

	rng := rand.New(rand.NewPCG(3, 9))
	for round := range 60 {
		// Unread the old profile's streams have handed over, waiting when the switch comes.
		before, err := client.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range idsOf(w.held()) {
			w.Unreads <- domain.Unread{RoomID: id, Messages: 1}
		}
		for deadline := time.Now().Add(settle); len(client.Unread()) < len(before) && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		var wg sync.WaitGroup
		for range 1 + rng.IntN(3) { // one switch, or several racing, back and forth
			to := profiles[rng.IntN(len(profiles))]
			wg.Go(func() { _ = client.SwitchProfile(to) })
		}
		if rng.IntN(2) == 0 { // unread in flight while the streams move
			w.Unreads <- domain.Unread{RoomID: idsOf(w.held())[rng.IntN(3)], Messages: 1}
		}
		wg.Wait()
		checkStreamsIn(t, fmt.Sprintf("round %d", round), client, w)
	}

	stopping := h.client()
	go func() { _ = stopping.Start(ctx) }()
	h.waitAttached(2)
	done := make(chan struct{})
	go func() { defer close(done); _ = stopping.SwitchProfile("telegram") }()
	stopping.Stop()
	select {
	case <-done:
	case <-time.After(settle):
		t.Fatal("a switch racing Stop never returned")
	}
}

// checkStreamsIn pushes one unread state per room the moment a switch returns, and
// checks the stream gives each room the client's profile holds and none other. A value
// still in flight from before may repeat one of them.
func checkStreamsIn(t *testing.T, what string, client *daemon.Remote, w *profileWorld) {
	t.Helper()
	for _, id := range idsOf(w.held()) {
		w.Unreads <- domain.Unread{RoomID: id, Messages: 1}
	}
	rooms, err := client.Rooms(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := roomsOf(rooms, roomOf)
	seen := map[domain.RoomID]bool{}
	quiet := time.After(settle)
	for len(seen) < len(want) {
		select {
		case u := <-client.Unread():
			seen[u.RoomID] = true
		case <-quiet:
			t.Fatalf("%s: the unread stream gave %v, want %v", what, slices.Sorted(maps.Keys(seen)), want)
		}
	}
	for done := false; !done; {
		select {
		case u := <-client.Unread():
			seen[u.RoomID] = true
		case <-time.After(30 * time.Millisecond):
			done = true
		}
	}
	if got := slices.Sorted(maps.Keys(seen)); !slices.Equal(got, want) {
		t.Errorf("%s: the unread stream gave %v, want %v", what, got, want)
	}
}

// profiledServe serves b with these profiles.
func profiledServe(t *testing.T, b api.Backend, profiles ...config.Profile) *harness {
	t.Helper()
	n, err := daemon.NewNotifications(config.Config{Profiles: profiles}, scopeOnly{b})
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, b, func(d *daemon.Daemon) { d.Notifications = n })
}

// windowIn is a window of h running in profile that asked for its seat.
func windowIn(t *testing.T, h *harness, profile string, pid int) (*daemon.Remote, error) {
	t.Helper()
	r := h.client()
	r.UseProfile(profile)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { r.Stop(); cancel() })
	err := r.TakeSeat(ctx, false, domain.SeatHolder{PID: pid, TTY: "/dev/pts/7"})
	return r, err
}

// Each profile has a window of its own: a second in the same profile, in any case, is
// refused and told where the first is; one in another profile sits beside it.
func TestEachProfileHasItsOwnWindow(t *testing.T) {
	t.Parallel()
	h := profiledServe(t, &draftStore{}, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}})
	if _, err := windowIn(t, h, "work", 101); err != nil {
		t.Fatalf("work's window: %v", err)
	}
	if _, err := windowIn(t, h, "home", 202); err != nil {
		t.Errorf("home's window beside work's: %v", err)
	}
	_, err := windowIn(t, h, "WORK", 303)
	if !errors.Is(err, api.ErrSeatTaken) || !strings.Contains(err.Error(), "pid 101") {
		t.Errorf("a second window in work = %v, want refused naming pid 101", err)
	}
}

// A window switching to a profile another window runs in is refused and stays where it
// was; to a free one, it takes that seat and leaves its old one free. Its draft writes
// count in the profile it runs in.
func TestASwitchMovesTheSeatOrIsRefused(t *testing.T) {
	t.Parallel()
	drafts := &draftStore{}
	h := profiledServe(t, drafts, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}}, config.Profile{Name: "spare", Include: []string{"*"}})
	a, err := windowIn(t, h, "work", 101)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := windowIn(t, h, "home", 202); err != nil {
		t.Fatal(err)
	}
	if err := a.SwitchProfile("home"); !errors.Is(err, api.ErrSeatTaken) {
		t.Fatalf("switching into home's window = %v, want ErrSeatTaken", err)
	}
	if _, err := windowIn(t, h, "work", 303); !errors.Is(err, api.ErrSeatTaken) {
		t.Errorf("after the refused switch, work = %v; want it still a's", err)
	}
	if err := a.SwitchProfile("spare"); err != nil {
		t.Fatalf("switching to spare: %v", err)
	}
	waitFor(t, func() bool { _, err := windowIn(t, h, "work", 303); return err == nil })
	if _, err := windowIn(t, h, "spare", 404); !errors.Is(err, api.ErrSeatTaken) {
		t.Errorf("a second window in spare = %v, want refused: a runs there", err)
	}
	draft := domain.StoredDraft{RoomID: mx + "!a:x", Body: "half"}
	if saved, err := a.ReplaceDraft(t.Context(), draft, domain.StoredDraft{}); err != nil || !saved {
		t.Errorf("a's draft in spare = %v, %v; want saved", saved, err)
	}
}

// Follow resolves the link among the cached rooms and hands it by the rule: a window
// with the room open beats one whose profile holds it, which beats the one used last; a
// link to nothing cached goes to the one used last.
func TestFollowHandsALinkToTheWindowPlacedForIt(t *testing.T) {
	t.Parallel()
	dm, group := domain.RoomID(mx+"!dm:x"), domain.RoomID(mx+"!group:x")
	w := &profileWorld{Nop: nopWithChannels()}
	w.set([]domain.Room{{ID: dm, IsDirect: true}, {ID: group}}, nil, nil)
	h := profiledServe(t, w, config.Profile{Name: "direct", Include: []string{"dm"}}, config.Profile{Name: "groups", Include: []string{"group"}})
	ctx := t.Context()
	open := func(profile string) *daemon.Remote {
		r := h.client()
		r.UseProfile(profile)
		go func() { _ = r.Start(ctx) }()
		t.Cleanup(r.Stop)
		return r
	}
	direct, groups := open("direct"), open("groups")
	h.waitAttached(2)
	// A switch to the profile it runs in returns once every stream is subscribed.
	for profile, r := range map[string]*daemon.Remote{"direct": direct, "groups": groups} {
		if err := r.SwitchProfile(profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := groups.Present(ctx, ""); err != nil { // groups was used last
		t.Fatal(err)
	}
	follow := func(uri string, want *daemon.Remote) {
		t.Helper()
		waitFor(t, func() bool { delivered, err := groups.Follow(ctx, uri); return err == nil && delivered })
		for _, r := range []*daemon.Remote{direct, groups} {
			select {
			case got := <-r.Follows():
				if r != want {
					t.Errorf("%s reached the wrong window", got)
				}
			case <-time.After(100 * time.Millisecond):
				if r == want {
					t.Errorf("%s never reached its window", uri)
				}
			}
		}
	}
	follow("matrix:roomid/dm:x", direct)               // its profile holds it
	follow("matrix:r/somewhere:x", groups)             // nothing cached: used last
	if err := direct.Present(ctx, group); err != nil { // direct has group open, and is used last
		t.Fatal(err)
	}
	follow("matrix:roomid/group:x", direct) // open beats the profile
}

// A link reaches a window with the chats it names in that window's profile, and only
// those: a link to a room outside it names none, so the window neither opens nor learns it.
func TestALinkNamesOnlyTheChatsOfTheWindowsProfile(t *testing.T) {
	t.Parallel()
	dm, group := domain.RoomID(mx+"!dm:x"), domain.RoomID(mx+"!group:x")
	w := &profileWorld{Nop: nopWithChannels()}
	w.set([]domain.Room{{ID: dm, IsDirect: true}, {ID: group}}, nil, nil)
	h := profiledServe(t, w, config.Profile{Name: "direct", Include: []string{"dm"}}, config.Profile{Name: "groups", Include: []string{"group"}})
	c := backendv1connect.NewBackendServiceClient(daemon.Dial(h.path), "http://kithd")
	listen := connect.NewRequest(&v1.FollowStreamRequest{Client: "w"})
	listen.Header().Set("Kith-Profile", "direct")
	st, err := c.FollowStream(t.Context(), listen)
	if err != nil {
		t.Fatal(err)
	}
	st.ResponseHeader() // listening
	for uri, want := range map[string][]string{
		"matrix:roomid/dm:x/e/evt": {string(dm) + " " + mx + "$evt"},
		"matrix:roomid/group:x":    {},
		"matrix:u/dana:x":          {},
	} {
		follow := connect.NewRequest(&v1.FollowRequest{Uri: uri})
		follow.Header().Set("Kith-Profile", "groups")
		if r, err := c.Follow(t.Context(), follow); err != nil || !r.Msg.GetDelivered() {
			t.Fatalf("follow %s: delivered %v, %v", uri, r.Msg.GetDelivered(), err)
		}
		if !st.Receive() {
			t.Fatalf("%s never reached the window: %v", uri, st.Err())
		}
		got := []string{}
		for _, chat := range st.Msg().GetChats() {
			got = append(got, chat.GetRoomId()+" "+chat.GetEventId())
		}
		if st.Msg().GetUri() != uri || !slices.Equal(got, want) {
			t.Errorf("%s reached the window as %s naming %v, want %v", uri, st.Msg().GetUri(), got, want)
		}
	}
}
