package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// railWorld is what kithd builds a rail from that the backend holds: rooms, invitations,
// spaces, unread counts, drafts, filter moves and canonical parents.
type railWorld struct {
	profileWorld

	mu       sync.Mutex
	unread   map[domain.RoomID]domain.Unread
	drafts   map[domain.RoomID]string
	verdicts map[domain.RoomID]map[string]domain.TagVerdict
	parents  map[domain.RoomID]domain.SpaceID
	asked    map[domain.RoomID]int
}

func newRailWorld() *railWorld {
	w := &railWorld{
		profileWorld: profileWorld{Nop: nopWithChannels()},
		unread:       map[domain.RoomID]domain.Unread{}, drafts: map[domain.RoomID]string{},
		verdicts: map[domain.RoomID]map[string]domain.TagVerdict{}, parents: map[domain.RoomID]domain.SpaceID{},
		asked: map[domain.RoomID]int{},
	}
	w.set(nil, nil, nil)
	return w
}

func (w *railWorld) CachedUnread(context.Context) ([]domain.Unread, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Collect(maps.Values(w.unread)), nil
}

func (w *railWorld) Drafts(context.Context) ([]domain.StoredDraft, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []domain.StoredDraft
	for id, body := range w.drafts {
		out = append(out, domain.StoredDraft{RoomID: id, Body: body})
	}
	return out, nil
}

func (w *railWorld) ReplaceDraft(_ context.Context, draft, _ domain.StoredDraft) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if draft.Body == "" {
		delete(w.drafts, draft.RoomID)
	} else {
		w.drafts[draft.RoomID] = draft.Body
	}
	return true, nil
}

func (w *railWorld) TagVerdicts(context.Context) ([]domain.TagVerdict, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []domain.TagVerdict
	for _, byTag := range w.verdicts {
		out = append(out, slices.Collect(maps.Values(byTag))...)
	}
	return out, nil
}

// MarkTag keeps a move or a release; a move never overwrites a release.
func (w *railWorld) MarkTag(_ context.Context, v domain.TagVerdict) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := strings.ToLower(v.Tag)
	if old, ok := w.verdicts[v.Room][key]; ok && old.Released && !v.Released {
		return domain.ErrReleased
	}
	if w.verdicts[v.Room] == nil {
		w.verdicts[v.Room] = map[string]domain.TagVerdict{}
	}
	w.verdicts[v.Room][key] = v
	return nil
}

func (w *railWorld) CanonicalParent(_ context.Context, id domain.RoomID) (domain.SpaceID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked[id]++
	return w.parents[id], nil
}

func (w *railWorld) setUnread(u domain.Unread) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unread[u.RoomID] = u
}

func (w *railWorld) asks() map[domain.RoomID]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.asked)
}

// fresh is the rail of profile name as a build from the world now gives it: what the
// served rail must equal once kithd has heard of every change.
func (w *railWorld) fresh(cfg config.Config, name string) domain.ServedRail {
	view := setup.ProfileConfig(cfg, name)
	places := setup.PlacesOf(view)
	spec := setup.RailSpec(places.Tags, view.Display.Rail, view.Display.Names)
	source, _ := setup.UnreadSource(cfg.Display.Unread)
	local := source != config.UnreadNotifications

	w.profileWorld.mu.Lock()
	rooms, spaces := slices.Concat(w.rooms, w.invites), slices.Clone(w.spaces)
	w.profileWorld.mu.Unlock()
	// The profile's rooms (TestAProfileSeesOnlyItsRooms checks the spaces it keeps).
	profiles, _ := setup.Profiles(cfg)
	if at := slices.IndexFunc(profiles, func(p domain.Profile) bool { return p.Name == name }); at >= 0 && !profiles[at].Everyone() {
		shared := setup.PlacesOf(cfg)
		rooms = slices.DeleteFunc(rooms, func(r domain.Room) bool {
			return !profiles[at].Holds(shared.Facts(r, domain.HoldersOf(r.ID, spaces)))
		})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	places.Order = places.Order.WithManaged(spaces)
	places.Caught = map[domain.RoomID][]string{}
	for id, byTag := range w.verdicts {
		for key, v := range byTag {
			if v.In() {
				places.Caught[id] = append(places.Caught[id], key)
			}
		}
		slices.Sort(places.Caught[id])
	}
	in := make([]domain.RailRoom, 0, len(rooms))
	for i := range rooms {
		id := rooms[i].ID
		count := w.unread[id]
		_, highlights := count.Count(local)
		in = append(in, domain.RailRoom{
			ID: id, Facts: places.Facts(rooms[i], domain.HoldersOf(id, spaces)),
			State: domain.RoomState{
				Unread: count.HasUnread(local), Mention: highlights > 0,
				Draft: w.drafts[id] != "", Invite: rooms[i].IsInvite(),
			},
			Parent: w.parents[id],
		})
	}
	return domain.BuildRail(spec, spaces, in).Served(spec, in)
}

// sameRail reports whether two rails hold the same rows and rooms, whatever their
// revisions.
func sameRail(a, b domain.ServedRail) bool {
	a.Revision, b.Revision = 0, 0
	return reflect.DeepEqual(a, b)
}

// railChanges counts the CHANGE_RAIL frames client is told.
func railChanges(t *testing.T, client *daemon.Remote) <-chan struct{} {
	t.Helper()
	out := make(chan struct{}, 1024)
	go func() {
		for c := range client.Changed() {
			if c == domain.ChangeRail {
				out <- struct{}{}
			}
		}
	}()
	return out
}

// railQuiet is how long no CHANGE_RAIL must come: past kithd's settle and a build.
const railQuiet = 150 * time.Millisecond

// servedOnce waits until the rail client is served equals want, and returns it.
func servedOnce(t *testing.T, client *daemon.Remote, want domain.ServedRail, what string) domain.ServedRail {
	t.Helper()
	deadline := time.Now().Add(settle)
	for {
		got, err := client.Rail(t.Context())
		if err == nil && sameRail(got, want) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the rail served is\n%+v (%v)\nbut a fresh build is\n%+v", what, got, err, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// told counts the CHANGE_RAIL frames that arrive until none has for railQuiet.
func told(changes <-chan struct{}) int {
	n := 0
	for {
		select {
		case <-changes:
			n++
		case <-time.After(railQuiet):
			return n
		}
	}
}

// The walk's rooms: two Matrix rooms (one direct), a Telegram chat its network archives,
// a room in both spaces; two invitations; the spaces, one a bridge keeps, and where each
// room was born.
var (
	railRooms = []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo", IsDirect: true},
		{ID: "telegram:1/100", Name: "Tele"}, {ID: mx + "!s:x", Name: "Shared"},
	}
	railInvites = []domain.Room{
		{ID: mx + "!i1:x", Name: "Inv one", Membership: domain.MembershipInvite, InvitedBy: "matrix:@ann:x"},
		{ID: mx + "!i2:x", Name: "Inv two", Membership: domain.MembershipInvite, InvitedBy: "matrix:@bo:x"},
	}
	railParents = map[domain.RoomID]domain.SpaceID{"telegram:1/100": mx + "!sb:x", mx + "!s:x": mx + "!sw:x", mx + "!a:x": mx + "!sw:x"}
)

func railSpaces(shape int) []domain.Space {
	work := domain.Space{ID: mx + "!sw:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x", mx + "!s:x"}}
	bridge := domain.Space{ID: mx + "!sb:x", Name: "Bridge", Keeper: "matrix:@bot:x", Children: []domain.RoomID{mx + "!s:x", mx + "!b:x", "telegram:1/100"}}
	switch shape {
	case 1:
		work.Children = append(work.Children, "telegram:1/100", railInvites[0].ID)
	case 2:
		return []domain.Space{bridge}
	case 3:
		return nil
	}
	return []domain.Space{work, bridge}
}

// railConfig is the config in one shape: tags reading every state word, silent and
// exclusive ones (spaces, origin: a parent decides), hidden and empty-hiding rows; the
// rail's lists; the Work space renamed; the priority; what a badge counts; and profile p
// with its own archive and rail in some shapes.
func railConfig(shape int, unread string) config.Config {
	no, yes := false, true
	tags := []config.Tag{
		{Name: "Invites", Rule: []string{"invite"}, First: true, CountInLabel: true, HideWhenEmpty: true},
		{Name: "Unread", Rule: []string{"unread"}, Sticky: true},
		{Name: "Mentions", Rule: []string{"mention"}, Exclusive: []string{config.ExclusiveTags}},
		{Name: "Drafts", Rule: []string{"draft"}, HideWhenEmpty: true},
		{Name: "Spam", Exclusive: []string{config.ExclusiveTags, config.ExclusiveSpaces}, CountsUnread: &no, HideWhenEmpty: true},
		{Name: "Quiet", Rule: []string{"room:Bravo"}, CountsUnread: &no, Exclusive: []string{config.ExclusiveSpaces}},
		{Name: "Archived", Rule: []string{"space:Nowhere"}, CountsUnread: &no,
			Exclusive: []string{config.ExclusiveTags, config.ExclusiveOrigin}, HideWhenEmpty: true},
		{Name: "All", Rule: []string{"*"}},
	}
	rails := []config.Rail{
		{Order: []string{"tag:Unread", "-", "*", "-", "tag:Archived"}, HideWhenEmpty: []string{"tag:Unread"}},
		{Order: []string{"place:Job", "tag:All", "--", "*"}, Hidden: []string{"tag:Mentions"}},
		{},
	}
	switch shape % 3 {
	case 1:
		tags[1].Exclusive = []string{config.ExclusiveTags}
		tags[2].Hidden = true
	case 2:
		tags[5].Rule = []string{"unread", "not dm"}
		tags[5].Exclusive = []string{config.ExclusiveOrigin}
	}
	p := config.Profile{Name: "p", Include: []string{"*"}}
	if shape%2 == 1 {
		p.Archive = config.ProfileArchive{Rooms: []string{"room:Alpha"}}
		p.Rail = rails[(shape+1)%len(rails)]
	}
	cfg := config.Config{
		Tags:     tags,
		Display:  config.Display{Rail: rails[shape%len(rails)], Unread: unread},
		Profiles: []config.Profile{{Name: "first", Include: []string{"*"}}, p},
		Telegram: config.Telegram{Archive: config.NetworkArchive{Follow: &yes}},
	}
	if shape%4 >= 2 {
		cfg.Display.Names = []config.DisplayName{{Target: config.GroupTarget("Work"), Name: "Job"}}
		cfg.Display.Priority = []string{"space:Bridge", "tag:Quiet"}
	}
	return cfg
}

// The rail a profile is served is the rail a fresh build gives over any sequence of
// changes to what it is built from: a reload (tags, rail lists, names, priority, what a
// badge counts, the profile's archive and rail), rooms rewritten, spaces refreshed,
// invitations coming and going, a filter's move and a release, a network archiving a room,
// unread counts and drafts, a canonical parent found, a Rail racing a reload, and changes
// that change nothing. After each, CHANGE_RAIL is told exactly when the rail differs from
// the one served before.
func TestTheServedRailHoldsOverAnyChangeSequence(t *testing.T) {
	t.Parallel()
	seeds, steps := 6, 24
	if testing.Short() {
		seeds, steps = 2, 12
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			walkRail(t, seed, steps)
		})
	}
}

func walkRail(t *testing.T, seed uint64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x7261696c))
	w := newRailWorld()
	w.set(slices.Clone(railRooms), railSpaces(0), nil)
	w.parents = maps.Clone(railParents)
	h, n := serveRailWorld(t, w)
	cfg := railConfig(0, "")
	mustReload(t, n, cfg)
	client := h.client()
	client.UseProfile("p")
	go func() { _ = client.Start(t.Context()) }()
	defer client.Stop()
	h.waitAttached(1)
	changes := railChanges(t, client)
	last := servedOnce(t, client, w.fresh(cfg, "p"), "start")
	told(changes)

	var log []string
	room := func() domain.Room {
		return slices.Concat(railRooms, railInvites)[rng.IntN(len(railRooms)+len(railInvites))]
	}
	ctx := t.Context()
	for step := range steps {
		switch rng.IntN(12) {
		case 0:
			cfg = railConfig(rng.IntN(8), cfg.Display.Unread)
			mustReload(t, n, cfg)
			log = append(log, "reload")
		case 1:
			cfg.Display.Unread = map[string]string{"": config.UnreadNotifications, config.UnreadNotifications: config.UnreadMessages,
				config.UnreadMessages: config.UnreadNotifications}[cfg.Display.Unread]
			mustReload(t, n, cfg)
			log = append(log, "unread counts "+cfg.Display.Unread)
		case 2:
			rooms := slices.Clone(railRooms)
			rooms[rng.IntN(len(rooms))].Name = []string{"Alpha", "Bravo", "Renamed"}[rng.IntN(3)]
			if rng.IntN(3) == 0 {
				rooms = rooms[:len(rooms)-1]
			}
			w.profileWorld.mu.Lock()
			w.rooms = rooms
			w.profileWorld.mu.Unlock()
			if rng.IntN(2) == 0 {
				n.InvalidateScope() // a network rewrote its rooms
			} else if _, err := client.RefreshRooms(ctx); err != nil {
				t.Fatal(err)
			}
			log = append(log, "rooms rewritten")
		case 3:
			shape := rng.IntN(4)
			w.profileWorld.mu.Lock()
			w.spaces = railSpaces(shape)
			w.profileWorld.mu.Unlock()
			if _, err := client.RefreshSpaces(ctx); err != nil {
				t.Fatal(err)
			}
			log = append(log, fmt.Sprintf("spaces shape %d", shape))
		case 4:
			var invites []domain.Room
			for i := range railInvites {
				if rng.IntN(2) == 0 {
					invites = append(invites, railInvites[i])
				}
			}
			w.profileWorld.mu.Lock()
			w.invites = invites
			w.profileWorld.mu.Unlock()
			w.Invs <- invites
			log = append(log, fmt.Sprintf("%d invitations", len(invites)))
		case 5:
			r := room()
			v := domain.TagVerdict{Room: r.ID, Tag: "Spam", Rule: domain.MoveFirstMessage, Filter: "crypto", At: time.Now()}
			if rng.IntN(2) == 0 {
				v = domain.TagVerdict{Room: r.ID, Tag: "spam", Released: true, At: time.Now()}
			}
			if err := client.MarkTag(ctx, v); err != nil && !errors.Is(err, domain.ErrReleased) {
				t.Fatal(err)
			}
			log = append(log, fmt.Sprintf("%s in Spam: released %v", r.ID, v.Released))
		case 6:
			w.profileWorld.mu.Lock()
			rooms := slices.Clone(w.rooms)
			for i := range rooms {
				if rooms[i].ID == "telegram:1/100" {
					rooms[i].Archived = !rooms[i].Archived
				}
			}
			w.rooms = rooms
			w.profileWorld.mu.Unlock()
			n.InvalidateScope() // the network said
			log = append(log, "telegram archived flips")
		case 7, 8:
			r := room()
			u := domain.Unread{RoomID: r.ID, Notifications: rng.IntN(3), Highlights: rng.IntN(2),
				Messages: rng.IntN(3), Mentions: rng.IntN(2), Counted: rng.IntN(4) > 0, Marked: rng.IntN(4) == 0}
			w.setUnread(u)
			w.Unreads <- u
			log = append(log, fmt.Sprintf("unread %+v", u))
		case 9:
			r := room()
			body := []string{"", "x"}[rng.IntN(2)]
			if _, err := client.ReplaceDraft(ctx, domain.StoredDraft{RoomID: r.ID, Body: body}, domain.StoredDraft{}); err != nil {
				t.Fatal(err)
			}
			log = append(log, fmt.Sprintf("draft in %s: %q", r.ID, body))
		case 10:
			// A Rail call racing a reload.
			next := railConfig(rng.IntN(8), cfg.Display.Unread)
			raced := make(chan struct{})
			go func() {
				defer close(raced)
				_, _ = client.Rail(ctx)
			}()
			mustReload(t, n, next)
			<-raced
			cfg = next
			log = append(log, "a Rail races a reload")
		default:
			// Nothing a rail is built from changes.
			mustReload(t, n, cfg)
			n.InvalidateScope()
			log = append(log, "nothing changes")
		}
		what := fmt.Sprintf("seed %d step %d\n  %s", seed, step, strings.Join(log, "\n  "))
		got := servedOnce(t, client, w.fresh(cfg, "p"), what)
		differs := !sameRail(last, got)
		if n := told(changes); differs != (n > 0) {
			t.Fatalf("%s: the rail differs %v from the one served before, and CHANGE_RAIL was told %d times", what, differs, n)
		}
		if got.Revision < last.Revision || (differs && got.Revision == last.Revision) {
			t.Fatalf("%s: revision %d after %d", what, got.Revision, last.Revision)
		}
		last = got
	}
}

func mustReload(t *testing.T, n *daemon.Notifications, cfg config.Config) {
	t.Helper()
	if err := n.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

// serveRailWorld serves w, its filters and parents included, with the notifier a test
// reloads.
func serveRailWorld(t *testing.T, w *railWorld) (*harness, *daemon.Notifications) {
	t.Helper()
	n, err := daemon.NewNotifications(asStarted(config.Config{}), scopeOnly{w})
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, w, func(d *daemon.Daemon) { d.Notifications = n }), n
}

// A profile a reload removes is refused its rail, its clients told; one of the same name
// added back holding other rooms is served those, nothing of the rail it had.
func TestAProfileRemovedAndAddedBackGetsItsOwnRail(t *testing.T) {
	t.Parallel()
	w := newRailWorld()
	w.set(slices.Clone(railRooms), railSpaces(3), nil)
	h, n := serveRailWorld(t, w)
	withP := func(include ...string) config.Config {
		return config.Config{Profiles: []config.Profile{{Name: "first", Include: []string{"*"}}, {Name: "p", Include: include}}}
	}
	mustReload(t, n, withP("room:Alpha"))
	client := h.client()
	client.UseProfile("p")
	go func() { _ = client.Start(t.Context()) }()
	defer client.Stop()
	h.waitAttached(1)
	changes := railChanges(t, client)
	rooms := func(rail domain.ServedRail) []string {
		var ids []string
		for _, r := range rail.Rooms {
			ids = append(ids, r.Facts.ID)
		}
		return ids
	}

	first, err := client.Rail(t.Context())
	if err != nil || !slices.Equal(rooms(first), []string{mx + "!a:x"}) {
		t.Fatalf("p's rail holds %v (%v), want Alpha alone", rooms(first), err)
	}
	told(changes)

	mustReload(t, n, config.Config{Profiles: []config.Profile{{Name: "first", Include: []string{"*"}}}})
	if told(changes) == 0 {
		t.Error("removing the profile told its clients nothing")
	}
	if _, err := client.Rail(t.Context()); !errors.Is(err, api.ErrUnknownProfile) {
		t.Errorf("Rail in a removed profile = %v, want ErrUnknownProfile", err)
	}

	mustReload(t, n, withP("room:Bravo"))
	again := servedOnce(t, client, w.fresh(withP("room:Bravo"), "p"), "added back")
	if !slices.Equal(rooms(again), []string{mx + "!b:x"}) || again.Revision <= first.Revision {
		t.Errorf("p added back is served %v at revision %d (after %d), want Bravo alone, newer", rooms(again), again.Revision, first.Revision)
	}
}

// CHANGE_RAIL goes to the clients of the profile whose rail changed, not to another's.
func TestARailChangeIsToldOnlyToItsProfile(t *testing.T) {
	t.Parallel()
	w := newRailWorld()
	w.set(slices.Clone(railRooms), railSpaces(3), nil)
	h, n := serveRailWorld(t, w)
	cfg := config.Config{
		Tags:     []config.Tag{{Name: "Unread", Rule: []string{"unread"}}, {Name: "All", Rule: []string{"*"}}},
		Profiles: []config.Profile{{Name: "p", Include: []string{"room:Alpha"}}, {Name: "q", Include: []string{"room:Bravo"}}},
	}
	mustReload(t, n, cfg)
	clients := map[string]*daemon.Remote{}
	heard := map[string]<-chan struct{}{}
	for _, name := range []string{"p", "q"} {
		c := h.client()
		c.UseProfile(name)
		go func() { _ = c.Start(t.Context()) }()
		t.Cleanup(c.Stop)
		clients[name] = c
	}
	h.waitAttached(2)
	for name, c := range clients {
		heard[name] = railChanges(t, c)
		servedOnce(t, c, w.fresh(cfg, name), name)
	}
	for name := range clients {
		told(heard[name])
	}

	u := domain.Unread{RoomID: mx + "!a:x", Messages: 1, Counted: true}
	w.setUnread(u)
	w.Unreads <- u
	servedOnce(t, clients["p"], w.fresh(cfg, "p"), "p after Alpha's unread")
	if told(heard["p"]) == 0 {
		t.Error("p was not told its rail changed")
	}
	if n := told(heard["q"]); n > 0 {
		t.Errorf("q was told %d times of p's rail", n)
	}
}

// A room's canonical parent is asked for only where it decides the rows: a tag holding
// it takes it from its spaces or its origin, not both; and once found it is not asked
// again. Found, it moves the room as a fresh build does.
func TestTheRailAsksForAParentOnlyWhereItDecides(t *testing.T) {
	t.Parallel()
	for _, exclusive := range [][]string{nil, {"spaces"}, {"origin"}, {"spaces", "origin"}} {
		w := newRailWorld()
		w.set([]domain.Room{{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"}}, []domain.Space{
			{ID: mx + "!w:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x", mx + "!b:x"}},
			{ID: mx + "!f:x", Name: "Filed", Children: []domain.RoomID{mx + "!a:x"}},
		}, nil)
		w.parents = map[domain.RoomID]domain.SpaceID{mx + "!a:x": mx + "!w:x"}
		h, n := serveRailWorld(t, w)
		cfg := config.Config{
			Tags:     []config.Tag{{Name: "Out", Picked: []string{mx + "!a:x"}, Exclusive: exclusive}},
			Profiles: []config.Profile{{Name: "p", Include: []string{"*"}}},
		}
		mustReload(t, n, cfg)
		client := h.client()
		client.UseProfile("p")
		servedOnce(t, client, w.fresh(cfg, "p"), fmt.Sprintf("exclusive %v", exclusive))
		for range 3 {
			n.InvalidateScope()
			servedOnce(t, client, w.fresh(cfg, "p"), fmt.Sprintf("exclusive %v, again", exclusive))
		}
		asks, want := w.asks(), map[domain.RoomID]int{}
		if len(exclusive) == 1 {
			want[mx+"!a:x"] = 1
		}
		if !maps.Equal(asks, want) {
			t.Errorf("exclusive %v: asked %v, want %v", exclusive, asks, want)
		}
	}
}
