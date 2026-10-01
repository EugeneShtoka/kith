package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// scopeSource is what resolving a notification scope needs from the backend.
type scopeSource interface {
	Rooms(ctx context.Context) ([]domain.Room, error)
	Spaces(ctx context.Context) ([]domain.Space, error)
	// ThreadParticipant reports whether we sent a thread's root or any reply.
	ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool
}

// scopeTTL is a backstop only: the index is invalidated on room-state changes
// (Refresher), config reloads and lookups for unknown rooms.
const scopeTTL = time.Hour

// roomFacts is everything a decision needs to know about where a message landed.
type roomFacts struct {
	name     string
	spaces   []string
	direct   bool
	protocol domain.Protocol
}

// scopeIndex answers "what do I know about this room?" from a cached read of the
// room list and space hierarchy, so a message does not cost two SQLite queries.
// Safe for concurrent use.
type scopeIndex struct {
	src scopeSource
	// log hears a failed rebuild (the previous index stays in use); set before use.
	log *slog.Logger

	mu      sync.Mutex
	aliases map[domain.RoomID]string
	// priority ranks spaces; {space} in a notification is the first.
	priority []string
	rooms    map[domain.RoomID]roomFacts
	builtAt  time.Time
	// missed records rooms a rebuild did not find, and when (see scopeRetry).
	missed map[domain.RoomID]time.Time
	// gen counts config changes; a rebuild (done unlocked) that raced one is discarded.
	gen    uint64
	pinned domain.Pinned
}

// newScopeIndex returns an index over src, with no rooms read yet.
func newScopeIndex(src scopeSource, aliases []config.DisplayName, priority []string) *scopeIndex {
	return &scopeIndex{src: src, aliases: roomAliases(aliases), priority: priority}
}

// SetAliases replaces the configured room names and drops the index.
func (x *scopeIndex) SetAliases(aliases []config.DisplayName) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.aliases = roomAliases(aliases)
	x.builtAt, x.missed = time.Time{}, nil
	x.gen++
}

// SetPinned replaces the pinned list, so rules naming `pinned` can match. No index
// drop: pinning is computed per lookup.
func (x *scopeIndex) SetPinned(entries []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.pinned = domain.Pinned{Entries: entries}
}

// SetSpacePriority replaces the space ranking and drops the index.
func (x *scopeIndex) SetSpacePriority(priority []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.priority = priority
	x.builtAt, x.missed = time.Time{}, nil
	x.gen++
}

// Invalidate drops the index (and the miss cooldown), so the next lookup rereads.
func (x *scopeIndex) Invalidate() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.builtAt, x.missed = time.Time{}, nil
	// A rebuild already reading must not install what it read before this.
	x.gen++
}

// Scope resolves the notification scope for a message. An unknown room forces a
// rebuild; if still unknown the scope carries the ID alone.
func (x *scopeIndex) Scope(ctx context.Context, roomID domain.RoomID, sender string, thread domain.EventID) (notify.Scope, bool) {
	facts, known := x.lookup(ctx, roomID)
	s := notify.Scope{
		Room:   setup.Place{Room: x.factsFrom(roomID, facts)},
		Sender: sender,
		Thread: string(thread),
	}
	// Only for thread messages: it is a cache query.
	if thread != "" {
		s.Participating = x.src.ThreadParticipant(ctx, roomID, thread)
	}
	return s, known
}

// Facts is the room as the RoomFacts vocabulary takes it. An unplaced room still
// answers with its ID: sync reaches a new room before /joined_rooms names it.
func (x *scopeIndex) Facts(ctx context.Context, roomID domain.RoomID) domain.RoomFacts {
	facts, ok := x.lookup(ctx, roomID)
	out := domain.RoomFacts{ID: string(roomID), Protocol: domain.NetworkOf(string(roomID))}
	if !ok {
		return out
	}
	return x.factsFrom(roomID, facts)
}

// factsFrom is one indexed room as the rule vocabulary takes it.
func (x *scopeIndex) factsFrom(roomID domain.RoomID, facts roomFacts) domain.RoomFacts {
	out := domain.RoomFacts{
		ID:       string(roomID),
		Name:     facts.name,
		Spaces:   facts.spaces,
		Direct:   facts.direct,
		Protocol: protocolOr(facts.protocol, roomID),
	}
	out.Pinned = x.pins().Pins(out)
	return out
}

// pins is the pinned list, read under the lock.
func (x *scopeIndex) pins() domain.Pinned {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.pinned
}

// protocolOr is the bridge's network, else the network the room's ID names (plain
// Matrix for a bare one), so a zero value never reads as a bridge.
func protocolOr(p domain.Protocol, roomID domain.RoomID) domain.Protocol {
	if p.IsBridged() {
		return p
	}
	return domain.NetworkOf(string(roomID))
}

// Direct reports whether a room is a direct message.
func (x *scopeIndex) Direct(ctx context.Context, roomID domain.RoomID) bool {
	facts, _ := x.lookup(ctx, roomID)
	return facts.direct
}

// scopeRetry is how long "this room is not in the cache" is believed before
// rebuilding again; without it every message in a not-yet-listed room cost several
// full cache reads.
const scopeRetry = 5 * time.Second

// lookup returns what is known about a room, rebuilding a stale or incomplete index
// first. The read happens with the lock released; gen guards the install.
func (x *scopeIndex) lookup(ctx context.Context, roomID domain.RoomID) (roomFacts, bool) {
	x.mu.Lock()
	facts, ok := x.rooms[roomID]
	if ok && time.Since(x.builtAt) < scopeTTL {
		x.mu.Unlock()
		return facts, true
	}
	// A room a recent rebuild already failed to find is not worth another read.
	if missedAt, seen := x.missed[roomID]; seen && time.Since(missedAt) < scopeRetry {
		x.mu.Unlock()
		return facts, ok
	}
	src, aliases, priority, gen := x.src, x.aliases, x.priority, x.gen
	x.mu.Unlock()

	index, err := buildIndex(ctx, src, aliases, priority)

	x.mu.Lock()
	defer x.mu.Unlock()
	// A failed read keeps the previous index.
	if err != nil && x.log != nil {
		x.log.Warn("notification scope: rebuild the room index failed; keeping the previous one", "room", roomID, "err", err)
	}
	current := gen == x.gen
	if err == nil && current {
		x.rooms, x.builtAt, x.missed = index, time.Now(), nil
	}
	facts, ok = x.rooms[roomID]
	// A miss is only believed from a read nothing has overtaken.
	if !ok && current {
		if x.missed == nil {
			x.missed = make(map[domain.RoomID]time.Time)
		}
		x.missed[roomID] = time.Now()
	}
	return facts, ok
}

// buildIndex reads the room list and space hierarchy into a fresh index. A free
// function, so it provably touches nothing the mutex guards.
func buildIndex(
	ctx context.Context,
	src scopeSource,
	aliases map[domain.RoomID]string,
	priority []string,
) (map[domain.RoomID]roomFacts, error) {
	rooms, err := src.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("scope: read rooms: %w", err)
	}
	spaces, err := src.Spaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("scope: read spaces: %w", err)
	}
	inSpaces := make(map[domain.RoomID][]string, len(rooms))
	// The network comes from the bridge space owning the room (or the room's own ID),
	// not senders' IDs.
	protocols := make(map[domain.RoomID]domain.Protocol, len(rooms))
	for i := range spaces {
		name := spaces[i].DisplayName()
		for _, child := range spaces[i].Children {
			inSpaces[child] = append(inSpaces[child], name)
			if spaces[i].Bridge.IsBridged() {
				protocols[child] = spaces[i].Bridge
			}
		}
	}
	index := make(map[domain.RoomID]roomFacts, len(rooms))
	for i := range rooms {
		index[rooms[i].ID] = roomFacts{
			name:     nameOf(aliases, rooms[i]),
			spaces:   domain.OrderSpaces(inSpaces[rooms[i].ID], priority),
			direct:   rooms[i].IsDirect,
			protocol: protocolOr(protocols[rooms[i].ID], rooms[i].ID),
		}
	}
	return index, nil
}

// nameOf is the name a rule matches a room by: the user's alias, else the room's own
// display name (never the shortened display label, which can change under rules).
func nameOf(aliases map[domain.RoomID]string, room domain.Room) string {
	if alias, ok := aliases[room.ID]; ok && alias != "" {
		return alias
	}
	return room.DisplayName()
}

// roomAliases indexes the configured room names by room ID.
func roomAliases(names []config.DisplayName) map[domain.RoomID]string {
	out := make(map[domain.RoomID]string, len(names))
	for room, name := range (config.Display{Names: names}).RoomNames() {
		out[domain.RoomID(room)] = name
	}
	return out
}
