package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The rail per profile (Rail): built here from the config and the rooms in their state
// now, so every client draws the same rows. The rail last served each profile is kept;
// every change to what it is built from moves a generation, and the server's loop
// (serveRails) builds each served profile's again and tells its clients (CHANGE_RAIL)
// when it differs.

// railSettle lets a burst of changes (a sync's unread counts) end in one rebuild.
const railSettle = 50 * time.Millisecond

// profileRail is what one profile's rail is built from besides the rooms: the places
// its facts are judged with (setup.ProfileConfig), its rail's lists and labels, and
// whether a badge counts messages (true) or notifications.
type profileRail struct {
	places domain.Places
	spec   domain.RailSpec
	local  bool
}

// profileRails is the config's profiles, and what each one's rail is built from by its
// lower-cased name.
func profileRails(cfg config.Config) ([]domain.Profile, map[string]profileRail, error) {
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		return nil, nil, err
	}
	source, err := setup.UnreadSource(cfg.Display.Unread)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]profileRail, len(profiles))
	for _, p := range profiles {
		view := setup.ProfileConfig(cfg, p.Name)
		places := setup.PlacesOf(view)
		out[strings.ToLower(p.Name)] = profileRail{
			places: places,
			spec:   setup.RailSpec(places.Tags, view.Display.Rail, view.Display.Names),
			local:  source != config.UnreadNotifications,
		}
	}
	return profiles, out, nil
}

// railCache is the rail last served each profile, the canonical parents found for it,
// and gen, which counts the changes to what a rail is built from: a rail built while
// gen moved is kept but never taken for current.
type railCache struct {
	// wake tells serveRails that gen moved or a parent is wanted; depth 1.
	wake chan struct{}

	mu  sync.Mutex
	gen uint64
	// rev is the last revision handed out. It starts at the boot time, so a client
	// holding a revision from before a restart takes the new kithd's.
	rev uint64
	// served is each profile's last rail, by lower-cased name.
	served map[string]servedRail
	// parents are the canonical parents found; wanted the rooms a build needs one for.
	parents map[domain.RoomID]domain.SpaceID
	wanted  map[domain.RoomID]bool
}

// servedRail is a rail served, the profile's name as the config spells it, and the
// generation it was built at.
type servedRail struct {
	name string
	gen  uint64
	rail domain.ServedRail
}

func newRailCache() *railCache {
	return &railCache{
		wake:    make(chan struct{}, 1),
		rev:     uint64(time.Now().UnixNano()), // #nosec G115 -- a time after 1970
		served:  map[string]servedRail{},
		parents: map[domain.RoomID]domain.SpaceID{},
		wanted:  map[domain.RoomID]bool{},
	}
}

// bump says something a rail is built from changed. Call it once the change is in
// place, so a build starting after it reads the change.
func (c *railCache) bump() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
	c.ring()
}

func (c *railCache) ring() {
	select {
	case c.wake <- struct{}{}:
	default: // already rung
	}
}

// current is the profile's rail when nothing it is built from changed since.
func (c *railCache) current(name string) (domain.ServedRail, bool, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.served[strings.ToLower(name)]
	return last.rail, ok && last.gen == c.gen, c.gen
}

// install keeps a rail built at gen as the profile's, unless one built later is kept
// already; changed says it differs from the one served before, under a new revision.
func (c *railCache) install(name string, gen uint64, built domain.ServedRail) (domain.ServedRail, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	last, had := c.served[key]
	switch {
	case had && last.gen > gen:
		return last.rail, false
	case had && sameRail(last.rail, built):
		last.gen = gen
		c.served[key] = last
		return last.rail, false
	}
	c.rev++
	built.Revision = c.rev
	c.served[key] = servedRail{name: name, gen: gen, rail: built}
	return built, true
}

// sameRail reports whether two rails hold the same rows and rooms, whatever their
// revisions.
func sameRail(a, b domain.ServedRail) bool {
	a.Revision, b.Revision = 0, 0
	return reflect.DeepEqual(a, b)
}

// forget drops a profile the config no longer has, reporting whether a rail was served
// it.
func (c *railCache) forget(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, had := c.served[strings.ToLower(name)]
	delete(c.served, strings.ToLower(name))
	return had
}

// servedNames is the profiles a rail was served, by name.
func (c *railCache) servedNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.served))
	for _, s := range c.served {
		names = append(names, s.name)
	}
	slices.Sort(names)
	return names
}

// knownParents is the canonical parents found so far.
func (c *railCache) knownParents() map[domain.RoomID]domain.SpaceID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.parents)
}

// want asks serveRails for the canonical parents of rooms not found yet.
func (c *railCache) want(rooms []domain.RoomID) {
	c.mu.Lock()
	added := false
	for _, id := range rooms {
		if _, known := c.parents[id]; !known && !c.wanted[id] {
			c.wanted[id], added = true, true
		}
	}
	c.mu.Unlock()
	if added {
		c.ring()
	}
}

// takeWanted is the rooms waiting for a canonical parent, no longer waiting: one not
// found is asked for again by the next build that needs it.
func (c *railCache) takeWanted() []domain.RoomID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.RoomID, 0, len(c.wanted))
	for id := range c.wanted {
		out = append(out, id)
	}
	clear(c.wanted)
	slices.Sort(out)
	return out
}

// found records canonical parents; finding any moves the generation.
func (c *railCache) found(parents map[domain.RoomID]domain.SpaceID) {
	if len(parents) == 0 {
		return
	}
	c.mu.Lock()
	maps.Copy(c.parents, parents)
	c.mu.Unlock()
	c.bump()
}

// Rail is the rail of the request's profile.
func (s *server) Rail(ctx context.Context, _ *req[v1.RailRequest]) (*resp[v1.RailResponse], error) {
	rail, err := s.rail(ctx, s.requestProfile(ctx))
	if err != nil {
		return nil, rpcErr(err)
	}
	return reply(pc.RailToProto(rail), nil)
}

// rail is the profile's rail: the one served last while nothing it is built from
// changed since, else one built now, its clients told when it differs. A profile the
// config no longer has is refused, and its clients told once.
func (s *server) rail(ctx context.Context, name string) (domain.ServedRail, error) {
	c := s.Notifications.rails
	last, current, gen := c.current(name)
	if current {
		return last, nil
	}
	built, wanted, err := s.buildRail(ctx, name)
	if errors.Is(err, api.ErrUnknownProfile) && c.forget(name) {
		s.Streams.railChanged(name)
	}
	if err != nil {
		return domain.ServedRail{}, err
	}
	c.want(wanted)
	served, changed := c.install(name, gen, built)
	if changed {
		s.Streams.railChanged(name)
	}
	return served, nil
}

// buildRail builds the profile's rail from the rooms, invitations, spaces, unread counts
// and drafts the profile holds now and the canonical parents found so far; wanted is the
// rooms whose rows wait on a parent not found yet.
func (s *server) buildRail(ctx context.Context, name string) (rail domain.ServedRail, wanted []domain.RoomID, err error) {
	p, err := s.Notifications.Profile(name)
	if err != nil {
		return domain.ServedRail{}, nil, err
	}
	cfg, ok := s.Notifications.profileRail(p.Name)
	if !ok {
		return domain.ServedRail{}, nil, fmt.Errorf("%w: %q", api.ErrUnknownProfile, name)
	}
	// Read as a request in the profile is: what it narrows to.
	ctx = domain.WithProfile(context.WithValue(ctx, profileKey{}, p.Name), p.Name)
	in, err := s.railInputs(ctx)
	if err != nil {
		return domain.ServedRail{}, nil, err
	}
	places := cfg.places
	places.Order = places.Order.WithManaged(in.spaces)
	places.Caught = map[domain.RoomID][]string{}
	parents := s.Notifications.rails.knownParents()
	rooms := make([]domain.RailRoom, 0, len(in.rooms))
	for i := range in.rooms {
		room := &in.rooms[i]
		if caught := s.Notifications.caught(room.ID); len(caught) > 0 {
			places.Caught[room.ID] = caught
		}
		count := in.unread[room.ID]
		_, highlights := count.Count(cfg.local)
		rooms = append(rooms, domain.RailRoom{
			ID:    room.ID,
			Facts: places.Facts(*room, domain.HoldersOf(room.ID, in.spaces)),
			State: domain.RoomState{
				Unread: count.HasUnread(cfg.local), Mention: highlights > 0,
				Draft: in.drafts[room.ID], Invite: room.IsInvite(),
			},
			Parent: parents[room.ID],
		})
	}
	built := domain.BuildRail(cfg.spec, in.spaces, rooms)
	for i := range rooms {
		if _, known := parents[rooms[i].ID]; !known && built.NeedsParent(rooms[i].ID) {
			wanted = append(wanted, rooms[i].ID)
		}
	}
	return built.Served(cfg.spec, rooms), wanted, nil
}

// railRead is what a rail is built from that the backend holds, as the request's
// profile has it.
type railRead struct {
	rooms  []domain.Room
	spaces []domain.Space
	unread map[domain.RoomID]domain.Unread
	drafts map[domain.RoomID]bool
}

// railInputs reads the rooms (joined, then invitations, as a client lists them), the
// spaces, the unread counts and the drafts the request's profile holds.
func (s *server) railInputs(ctx context.Context) (railRead, error) {
	joined, err := s.Backend.Rooms(ctx)
	if err != nil {
		return railRead{}, fmt.Errorf("read the rooms: %w", err)
	}
	invites, err := s.Backend.CachedInvites(ctx)
	if err != nil {
		return railRead{}, fmt.Errorf("read the invitations: %w", err)
	}
	spaces, err := s.Backend.Spaces(ctx)
	if err != nil {
		return railRead{}, fmt.Errorf("read the spaces: %w", err)
	}
	counts, err := s.Backend.CachedUnread(ctx)
	if err != nil {
		return railRead{}, fmt.Errorf("read the unread counts: %w", err)
	}
	counts, err = s.profileUnread(ctx, profileKeeps(s.profileHolds(ctx), counts, func(u domain.Unread) domain.RoomID { return u.RoomID }))
	if err != nil {
		return railRead{}, fmt.Errorf("count the unread: %w", err)
	}
	stored, err := s.Backend.Drafts(ctx)
	if err != nil {
		return railRead{}, fmt.Errorf("read the drafts: %w", err)
	}
	in := railRead{
		rooms:  s.profileRooms(ctx, slices.Concat(joined, invites)),
		spaces: s.profileSpaces(ctx, spaces),
		unread: make(map[domain.RoomID]domain.Unread, len(counts)),
		drafts: make(map[domain.RoomID]bool, len(stored)),
	}
	for _, u := range counts {
		in.unread[u.RoomID] = u
	}
	for i := range stored {
		in.drafts[stored[i].RoomID] = true
	}
	return in, nil
}

// serveRails builds each served profile's rail again once what it is built from
// changed, telling its clients when it differs, and finds the canonical parents builds
// wait on. The unread counts and the invitations change with no call, so their
// streams move the generation. It returns when ctx ends.
func (s *server) serveRails(ctx context.Context) {
	c := s.Notifications.rails
	unreadID, unread := s.Streams.unread.subscribeWith(subBuffer)
	defer s.Streams.unread.release(unreadID)
	invitesID, invites := s.Streams.invites.subscribeWith(subBuffer)
	defer s.Streams.invites.release(invitesID)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-unread:
			if !ok {
				unread = nil // ended with the backend: no more counts
				continue
			}
			c.bump()
		case _, ok := <-invites:
			if !ok {
				invites = nil
				continue
			}
			c.bump()
		case <-c.wake:
			if !sleepUntil(ctx, railSettle) {
				return
			}
			s.findParents(ctx)
			for _, name := range c.servedNames() {
				if _, err := s.rail(ctx, name); err != nil && !errors.Is(err, api.ErrUnknownProfile) && ctx.Err() == nil {
					s.Log.Warn("build a profile's rail", "profile", name, "err", err)
				}
			}
		}
	}
}

// startRails runs serveRails until ctx ends; wait returns once it has.
func (s *server) startRails(ctx context.Context) (wait func()) {
	var running sync.WaitGroup
	running.Go(func() { s.serveRails(ctx) })
	return running.Wait
}

// findParents asks for the canonical parents builds wait on; one that fails is asked for
// again by the next build needing it.
func (s *server) findParents(ctx context.Context) {
	found := map[domain.RoomID]domain.SpaceID{}
	for _, id := range s.Notifications.rails.takeWanted() {
		parent, err := s.Backend.CanonicalParent(ctx, id)
		if err != nil {
			continue
		}
		found[id] = parent
	}
	s.Notifications.rails.found(found)
}
