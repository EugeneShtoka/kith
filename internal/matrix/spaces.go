package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

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
	spaces, err := b.matrixSpaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: read cached spaces: %w", err)
	}
	return spaces, nil
}

// matrixSpaces is the cached Matrix spaces; the cache holds every network's.
func (b *InProc) matrixSpaces(ctx context.Context) ([]domain.Space, error) {
	spaces, err := b.cache.Spaces(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // each caller says what it was reading for
	}
	return slices.DeleteFunc(spaces, func(sp domain.Space) bool { return !domain.MatrixRooms.Owns(domain.RoomID(sp.ID)) }), nil
}

// RefreshSpaces fetches, caches and returns the joined spaces with their direct
// non-space children. An unreadable space keeps its cached form: its rooms scope
// agent access, so a failed read must not look like an empty space.
func (b *InProc) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	asked := time.Now()
	resp, err := b.client.JoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("matrix: joined rooms: %w", err)
	}
	// Drop rooms left since the list was asked for.
	resp.JoinedRooms = slices.DeleteFunc(resp.JoinedRooms, func(roomID id.RoomID) bool {
		return b.left.since(domain.RoomID(roomID), asked)
	})
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

	// Asked once for all spaces (see domain.Space.Keeper).
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
		if err := b.cache.SaveSpaces(ctx, domain.MatrixRooms, spaces); err != nil {
			return nil, fmt.Errorf("matrix: cache spaces: %w", err)
		}
		// Original derives from canonical parents: computed on read, not stored.
		if origins, err := b.cache.OriginSpaces(ctx); err == nil {
			for i := range spaces {
				spaces[i].Original = origins[spaces[i].ID]
			}
		}
	}
	return spaces, nil
}

// readCreateEvents reads every room's m.room.create concurrently.
func (b *InProc) readCreateEvents(ctx context.Context, joined []id.RoomID) (isSpace, read []bool, predecessors []domain.RoomID) {
	isSpace, read = make([]bool, len(joined)), make([]bool, len(joined))
	predecessors = make([]domain.RoomID, len(joined))
	fanOut(len(joined), func(i int) {
		isSpace[i], predecessors[i], read[i] = b.roomCreate(ctx, joined[i])
	})
	return isSpace, read, predecessors
}

// knownSpaces is the cached spaces by ID; empty when there is no readable cache.
func (b *InProc) knownSpaces(ctx context.Context) map[domain.SpaceID]domain.Space {
	out := map[domain.SpaceID]domain.Space{}
	if b.cache == nil {
		return out
	}
	cached, err := b.matrixSpaces(ctx)
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

// saveUpgradeLinks records each upgrade link both ways: backward for scrollback and
// search, forward for navigation. Best-effort.
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

// roomCreate reads m.room.create; read is false when it could not be read.
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
		children = append(children, evt) // ownership counts every child, joined or not
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
	space.Leaving = leavingOf(state)
	space.Bridge = spaceOwner(creator, children)
	space.Keeper = spaceKeeper(state[event.StateMember], string(b.client.UserID), wide)
	// Sorted: children arrive in map order and downstream compares lists.
	slices.Sort(space.Children)
	return space, true
}

// personalSpace marks mautrix's per-login "personal filtering space".
const personalSpace = "__personal_filtering_space__"

// telegramForum matches the Telegram bridge's forum space ID (channel, topic -1).
// Leaving any topic leaves the whole forum, unlike a community ("channel:<id>").
var telegramForum = regexp.MustCompile(`^channel:-?\d+:-1$`)

// leavingOf reads how a space is left: a bridge's personal space only by signing
// out, a Telegram forum whole with its topics, anything else on its own.
func leavingOf(state map[event.Type]map[string]*event.Event) domain.SpaceLeaving {
	for _, kind := range []event.Type{event.StateBridge, event.StateHalfShotBridge} {
		for _, evt := range state[kind] {
			if evt == nil {
				continue
			}
			var info struct {
				Protocol struct {
					ID string `json:"id"`
				} `json:"protocol"`
				Channel struct {
					ID string `json:"id"`
				} `json:"channel"`
				RoomType string `json:"com.beeper.room_type.v2"`
			}
			if json.Unmarshal(evt.Content.VeryRaw, &info) != nil {
				continue
			}
			switch {
			case info.Channel.ID == personalSpace || info.RoomType == "personal_filtering_space":
				return domain.NotLeft
			case info.Protocol.ID == "telegram" && telegramForum.MatchString(info.Channel.ID):
				return domain.LeftWhole
			}
		}
	}
	return domain.LeftAlone
}

// spaceOwner returns the bridge that created the space or wrote every live child
// event; ProtocolMatrix otherwise. Blanked child events do not count.
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

// CanonicalParent resolves the room's canonical m.space.parent (set by bridges)
// among cached spaces; the answer, including "none", is cached.
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
	spaces, err := b.matrixSpaces(ctx)
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

// RemoveFromSpace blanks the space's m.space.child and the room's m.space.parent.
// A refused parent returns ErrNoSpaceParent: the room is already out.
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

// AddToSpace writes the space's m.space.child and the room's m.space.parent; a
// refused parent returns ErrNoSpaceParent. Canonical stays unset: it marks a room's
// single origin (see domain.Space.Original).
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
