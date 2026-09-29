package matrix

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Spaces returns the cached spaces, or — with no cache — a live RefreshSpaces.
func (b *InProc) Spaces(ctx context.Context) ([]domain.Space, error) {
	if b.cache == nil {
		return b.RefreshSpaces(ctx)
	}
	spaces, err := b.cache.Spaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: read cached spaces: %w", err)
	}
	return spaces, nil
}

// RefreshSpaces fetches the joined spaces with their direct child rooms (sub-spaces
// excluded), caches and returns them. A space whose state cannot be read keeps what
// the cache knew of it: its rooms decide what an agent may see (`except = ["space:…"]`),
// so a failed read must not read as a space with no rooms. A room whose kind cannot
// be read is a space if the cache says so.
func (b *InProc) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	resp, err := b.client.JoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: joined rooms: %w", err)
	}
	joined := make(map[id.RoomID]bool, len(resp.JoinedRooms))
	for _, roomID := range resp.JoinedRooms {
		joined[roomID] = true
	}

	isSpace, read, predecessors := b.readCreateEvents(ctx, resp.JoinedRooms)
	b.saveUpgradeLinks(ctx, resp.JoinedRooms, predecessors)

	known := b.knownSpaces(ctx)
	spaceIDs := make(map[id.RoomID]bool)
	for i, roomID := range resp.JoinedRooms {
		if !read[i] {
			_, isSpace[i] = known[domain.SpaceID(roomID)]
		}
		if isSpace[i] {
			spaceIDs[roomID] = true
		}
	}

	// Who reaches across your rooms, asked once for all spaces (see domain.Space.Keeper).
	wide := map[string]bool{}
	if b.cache != nil {
		if found, err := b.cache.WideReachingUsers(ctx, string(b.client.UserID), domain.KeeperReach); err == nil {
			wide = found
		}
	}

	spaces := make([]domain.Space, 0, len(spaceIDs))
	for i, roomID := range resp.JoinedRooms {
		if !isSpace[i] {
			continue
		}
		if space, ok := b.spaceChildren(ctx, roomID, joined, spaceIDs, wide); ok {
			spaces = append(spaces, space)
		} else if cached, had := known[domain.SpaceID(roomID)]; had {
			spaces = append(spaces, cached)
		}
	}
	domain.SortSpaces(spaces)
	if b.cache != nil {
		if err := b.cache.SaveSpaces(ctx, spaces); err != nil {
			return nil, fmt.Errorf("matrix: cache spaces: %w", err)
		}
		// Original is derived from canonical parents, so it is computed on read, not stored.
		if origins, err := b.cache.OriginSpaces(ctx); err == nil {
			for i := range spaces {
				spaces[i].Original = origins[spaces[i].ID]
			}
		}
	}
	return spaces, nil
}

// readCreateEvents reads every joined room's m.room.create concurrently: which rooms
// are spaces, which could be read at all, and which room each replaced.
func (b *InProc) readCreateEvents(ctx context.Context, joined []id.RoomID) (isSpace, read []bool, predecessors []domain.RoomID) {
	isSpace, read = make([]bool, len(joined)), make([]bool, len(joined))
	predecessors = make([]domain.RoomID, len(joined))
	fanOut(len(joined), func(i int) {
		isSpace[i], predecessors[i], read[i] = b.roomCreate(ctx, joined[i])
	})
	return isSpace, read, predecessors
}

// knownSpaces is the cached spaces by ID; none without a cache, or when it cannot be
// read (the refresh then knows only what it read).
func (b *InProc) knownSpaces(ctx context.Context) map[domain.SpaceID]domain.Space {
	out := map[domain.SpaceID]domain.Space{}
	if b.cache == nil {
		return out
	}
	cached, err := b.cache.Spaces(ctx)
	b.warnIf(ctx, err, "read cached spaces")
	for i := range cached {
		out[cached[i].ID] = cached[i]
	}
	return out
}

// fanOut runs fn(0..n-1) on at most roomNameWorkers goroutines and waits.
func fanOut(n int, fn func(i int)) {
	sem := make(chan struct{}, roomNameWorkers)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

// saveUpgradeLinks records each predecessor link in both directions: backward on the
// successor (scrollback, search chain) and forward on the predecessor (navigation).
// Best-effort.
func (b *InProc) saveUpgradeLinks(ctx context.Context, joined []id.RoomID, predecessors []domain.RoomID) {
	if b.cache == nil {
		return
	}
	for i, predecessor := range predecessors {
		if predecessor == "" {
			continue
		}
		successor := domain.RoomID(joined[i])
		b.warnIf(ctx, b.cache.SaveRoomUpgrade(ctx, successor, db.RoomUpgrade{Predecessor: predecessor}),
			"cache room upgrade", "room", successor, "predecessor", predecessor)
		b.warnIf(ctx, b.cache.SaveRoomUpgrade(ctx, predecessor, db.RoomUpgrade{Replacement: successor}),
			"cache room upgrade", "room", predecessor, "replacement", successor)
	}
}

// roomCreate reads m.room.create: whether the room is a space, and its predecessor;
// read is false when it could not be read (neither is known then).
func (b *InProc) roomCreate(ctx context.Context, roomID id.RoomID) (isSpace bool, predecessor domain.RoomID, read bool) {
	var content event.CreateEventContent
	if err := b.client.StateEvent(ctx, roomID, event.StateCreate, "", &content); err != nil {
		b.warnIf(ctx, err, "read m.room.create", "room", roomID)
		return false, "", false
	}
	return content.Type == event.RoomTypeSpace, domain.RoomID(content.GetPredecessor().RoomID), true
}

// spaceChildren resolves a space's name and joined, non-space children from the
// space's own state. /hierarchy is not used: tuwunel returns incomplete, varying
// results and caps limit at 100. Left rooms' child events stay in state forever,
// hence the joined filter.
func (b *InProc) spaceChildren(ctx context.Context, spaceID id.RoomID, joined, spaces map[id.RoomID]bool, wide map[string]bool) (domain.Space, bool) {
	space := domain.Space{ID: domain.SpaceID(spaceID)}
	state, err := b.client.State(ctx, spaceID)
	if err != nil {
		b.warnIf(ctx, err, "read space state", "space", spaceID)
		return space, false
	}
	if named := state[event.StateRoomName][""]; named != nil {
		if content := named.Content.AsRoomName(); content != nil {
			space.Name = content.Name
		}
	}
	var creator string
	if created := state[event.StateCreate][""]; created != nil {
		creator = string(created.Sender)
	}
	children := make([]*event.Event, 0, len(state[event.StateSpaceChild]))
	for stateKey, evt := range state[event.StateSpaceChild] {
		// Every child event counts toward ownership, joined or not.
		children = append(children, evt)
		// A removed child is a blanked event, not a deleted one.
		if evt == nil || len(evt.Content.VeryRaw) <= 2 {
			continue
		}
		child := id.RoomID(stateKey)
		if !joined[child] || spaces[child] {
			continue
		}
		space.Children = append(space.Children, domain.RoomID(child))
	}
	space.Bridge = spaceOwner(creator, children)
	space.Keeper = spaceKeeper(state[event.StateMember], string(b.client.UserID), wide)
	// Sorted: children arrive in map order and downstream compares lists.
	slices.Sort(space.Children)
	return space, true
}

// spaceOwner returns the bridge network that owns a space (ProtocolMatrix for a
// person's): it created the space, or every live child event was written by that one
// bridge. Blanked (removed) child events are not evidence.
func spaceOwner(creator string, children []*event.Event) domain.Protocol {
	if p := domain.ProtocolOf(creator); p.IsBridged() {
		return p
	}
	live, byBridge := 0, 0
	filler := domain.ProtocolMatrix
	for _, evt := range children {
		if evt == nil || len(evt.Content.VeryRaw) <= 2 {
			continue
		}
		live++
		p := domain.ProtocolOf(string(evt.Sender))
		if p.IsBridged() && (filler == domain.ProtocolMatrix || filler == p) {
			byBridge++
			filler = p
		}
	}
	if live > 0 && byBridge == live {
		return filler
	}
	return domain.ProtocolMatrix
}

// spaceKeeper returns a joined member other than self who also reaches widely across
// your rooms (see domain.Space.Keeper); "" when none.
func spaceKeeper(members map[string]*event.Event, self string, wide map[string]bool) string {
	for stateKey, evt := range members {
		if stateKey == self || evt == nil {
			continue
		}
		if evt.Content.AsMember().Membership != event.MembershipJoin {
			continue
		}
		if wide[stateKey] {
			return stateKey
		}
	}
	return ""
}

// CanonicalParent resolves the space a room lives in: the m.space.parent with
// canonical: true (set by bridges). Candidates come from the cached hierarchy, and
// the answer, including "none", is cached.
func (b *InProc) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	if b.cache != nil {
		parent, known, err := b.cache.RoomParent(ctx, roomID)
		if err == nil && known {
			return parent, nil
		}
		b.warnIf(ctx, err, "read cached room parent", "room", roomID)
	}
	parent := b.lookupParent(ctx, roomID)
	if b.cache != nil {
		b.warnIf(ctx, b.cache.SaveRoomParent(ctx, roomID, parent), "cache room parent", "room", roomID)
	}
	return parent, nil
}

// lookupParent asks which of a room's cached spaces it canonically claims, or "".
func (b *InProc) lookupParent(ctx context.Context, roomID domain.RoomID) domain.SpaceID {
	for _, spaceID := range b.spacesHolding(ctx, roomID) {
		var content event.SpaceParentEventContent
		err := b.client.StateEvent(ctx, id.RoomID(roomID), event.StateSpaceParent, string(spaceID), &content)
		if err != nil {
			continue // no such state event, or the room is unreadable: not this space
		}
		if content.Canonical {
			return spaceID
		}
	}
	return ""
}

// spacesHolding lists the cached spaces that hold roomID.
func (b *InProc) spacesHolding(ctx context.Context, roomID domain.RoomID) []domain.SpaceID {
	if b.cache == nil {
		return nil
	}
	spaces, err := b.cache.Spaces(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached spaces", "room", roomID)
		return nil
	}
	var holding []domain.SpaceID
	for i := range spaces {
		if slices.Contains(spaces[i].Children, roomID) {
			holding = append(holding, spaces[i].ID)
		}
	}
	return holding
}

// cachedSpaceIDs is the joined rooms known to be spaces; empty on a cold cache.
func (b *InProc) cachedSpaceIDs(ctx context.Context) map[domain.RoomID]bool {
	if b.cache == nil {
		return nil
	}
	ids, err := b.cache.SpaceIDs(ctx)
	if err != nil {
		b.warnIf(ctx, err, "read cached space IDs")
		return nil
	}
	return ids
}

// RemoveFromSpace empties the space's m.space.child and the room's m.space.parent
// (state is unsaid by empty content). A refused parent returns ErrNoSpaceParent:
// the room is already out. The cached canonical parent is untouched.
func (b *InProc) RemoveFromSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	empty := struct{}{}
	if _, err := b.client.SendStateEvent(ctx, id.RoomID(spaceID), event.StateSpaceChild, string(roomID), &empty); err != nil {
		return fmt.Errorf("matrix: remove %s from space %s: %w", roomID, spaceID, err)
	}
	if _, err := b.client.SendStateEvent(ctx, id.RoomID(roomID), event.StateSpaceParent, string(spaceID), &empty); err != nil {
		return fmt.Errorf("%w: %w", api.ErrNoSpaceParent, err)
	}
	return nil
}

// AddToSpace writes the space's m.space.child and the room's non-canonical
// m.space.parent. Each needs power in its own room; a refused parent returns
// ErrNoSpaceParent since the room is already filed. Canonical is deliberately unset:
// it marks a room's single origin (see domain.Space.Original).
func (b *InProc) AddToSpace(ctx context.Context, spaceID domain.SpaceID, roomID domain.RoomID) error {
	via := []string{b.client.UserID.Homeserver()}
	_, err := b.client.SendStateEvent(ctx, id.RoomID(spaceID), event.StateSpaceChild, string(roomID),
		&event.SpaceChildEventContent{Via: via})
	if err != nil {
		return fmt.Errorf("matrix: add %s to space %s: %w", roomID, spaceID, err)
	}
	if _, err := b.client.SendStateEvent(ctx, id.RoomID(roomID), event.StateSpaceParent, string(spaceID),
		&event.SpaceParentEventContent{Via: via}); err != nil {
		return fmt.Errorf("%w: %w", api.ErrNoSpaceParent, err)
	}
	return nil
}
