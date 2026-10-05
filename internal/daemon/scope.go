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

// scopeIndex answers "what do I know about this room?" from a cached read of the
// room list and space hierarchy, so a message does not cost two SQLite queries.
// Safe for concurrent use.
type scopeIndex struct {
	src scopeSource
	// log hears a failed rebuild (the previous index stays in use); set before use.
	log *slog.Logger

	mu      sync.Mutex
	aliases map[domain.RoomID]string
	// order ranks a room's homes; {space} in a notification is the first. base is the
	// config's part of it, and order base with the network's own spaces the last
	// rebuild read marked.
	base, order domain.HomeOrder
	// rooms are the indexed rooms' facts, tags left out: tags change without a
	// rebuild, so they are applied as a room is looked up.
	rooms   map[domain.RoomID]domain.RoomFacts
	builtAt time.Time
	// missed records rooms a rebuild did not find, and when (see scopeRetry).
	missed map[domain.RoomID]time.Time
	// gen counts config changes; a rebuild (done unlocked) that raced one is discarded.
	gen uint64
	// tags are judged per lookup, so a reload of them needs no rebuild; so is each
	// network's archive (archives, the tag each followed one is), over the rooms the
	// last rebuild read as archived by their network.
	tags     domain.TagSet
	archives map[domain.Protocol]string
	archived map[domain.RoomID]bool
}

// newScopeIndex returns an index over src, with no rooms read yet.
func newScopeIndex(src scopeSource, aliases []config.DisplayName, order domain.HomeOrder) *scopeIndex {
	return &scopeIndex{src: src, aliases: roomAliases(aliases), base: order, order: order}
}

// SetAliases replaces the configured room names and drops the index.
func (x *scopeIndex) SetAliases(aliases []config.DisplayName) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.aliases = roomAliases(aliases)
	x.builtAt, x.missed = time.Time{}, nil
	x.gen++
}

// SetTags replaces the tags, so rules naming tag:<name> can match. No index drop:
// tags are judged per lookup.
func (x *scopeIndex) SetTags(tags domain.TagSet) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.tags = tags
}

// SetArchives replaces the tag each followed network's archive is. No index drop:
// it is judged per lookup.
func (x *scopeIndex) SetArchives(archives map[domain.Protocol]string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.archives = archives
}

// Home is a room's first home — space or tag, in home order — as {space} shows it.
func (x *scopeIndex) Home(facts domain.RoomFacts) string {
	x.mu.Lock()
	order := x.order
	x.mu.Unlock()
	homes := domain.Homes(facts.Spaces, facts.Tags, order)
	if len(homes) == 0 {
		return ""
	}
	return domain.HomeLabel(homes[0])
}

// SetHomeOrder replaces the config's part of the home order and drops the index.
func (x *scopeIndex) SetHomeOrder(order domain.HomeOrder) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.base, x.order = order, order
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

// Known is the room as the index holds it now, with today's tags, never rebuilding it;
// a room it does not hold is its ID and network alone.
func (x *scopeIndex) Known(roomID domain.RoomID) domain.RoomFacts {
	x.mu.Lock()
	facts := x.rooms[roomID]
	x.mu.Unlock()
	return x.factsFrom(roomID, facts)
}

// factsFrom is one indexed room as the rule vocabulary takes it, with today's tags.
func (x *scopeIndex) factsFrom(roomID domain.RoomID, facts domain.RoomFacts) domain.RoomFacts {
	if facts.ID == "" {
		facts = domain.RoomFacts{ID: string(roomID), Protocol: domain.NetworkOf(string(roomID))}
	}
	x.mu.Lock()
	tags := x.tags
	if x.archived[roomID] {
		facts.ArchivedIn = x.archives[facts.Protocol]
	}
	x.mu.Unlock()
	facts.Tags = tags.Of(facts)
	return facts
}

// Direct reports whether a room is a direct message.
func (x *scopeIndex) Direct(ctx context.Context, roomID domain.RoomID) bool {
	facts, _ := x.lookup(ctx, roomID)
	return facts.Direct
}

// scopeRetry is how long "this room is not in the cache" is believed before
// rebuilding again; without it every message in a not-yet-listed room cost several
// full cache reads.
const scopeRetry = 5 * time.Second

// lookup returns what is known about a room, rebuilding a stale or incomplete index
// first. The read happens with the lock released; gen guards the install.
func (x *scopeIndex) lookup(ctx context.Context, roomID domain.RoomID) (domain.RoomFacts, bool) {
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
	src, aliases, base, gen := x.src, x.aliases, x.base, x.gen
	x.mu.Unlock()

	index, archived, order, err := buildIndex(ctx, src, aliases, base)

	x.mu.Lock()
	defer x.mu.Unlock()
	// A failed read keeps the previous index.
	if err != nil && x.log != nil {
		x.log.Warn("notification scope: rebuild the room index failed; keeping the previous one", "room", roomID, "err", err)
	}
	current := gen == x.gen
	if err == nil && current {
		x.rooms, x.archived, x.order, x.builtAt, x.missed = index, archived, order, time.Now(), nil
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

// buildIndex reads the room list and space hierarchy into a fresh index, the rooms
// their network archived, and the home order with the network's own spaces among them marked. A free function, so it
// provably touches nothing the mutex guards.
func buildIndex(
	ctx context.Context,
	src scopeSource,
	aliases map[domain.RoomID]string,
	base domain.HomeOrder,
) (map[domain.RoomID]domain.RoomFacts, map[domain.RoomID]bool, domain.HomeOrder, error) {
	rooms, err := src.Rooms(ctx)
	if err != nil {
		return nil, nil, base, fmt.Errorf("scope: read rooms: %w", err)
	}
	spaces, err := src.Spaces(ctx)
	if err != nil {
		return nil, nil, base, fmt.Errorf("scope: read spaces: %w", err)
	}
	order := base.WithManaged(spaces)
	holders := make(map[domain.RoomID][]domain.Space, len(rooms))
	for i := range spaces {
		for _, child := range spaces[i].Children {
			holders[child] = append(holders[child], spaces[i])
		}
	}
	places := domain.Places{Names: aliases, Order: order}
	index := make(map[domain.RoomID]domain.RoomFacts, len(rooms))
	archived := map[domain.RoomID]bool{}
	for i := range rooms {
		index[rooms[i].ID] = places.Facts(rooms[i], holders[rooms[i].ID])
		if rooms[i].Archived {
			archived[rooms[i].ID] = true
		}
	}
	return index, archived, order, nil
}

// roomAliases indexes the configured room names by room ID.
func roomAliases(names []config.DisplayName) map[domain.RoomID]string {
	out := make(map[domain.RoomID]string, len(names))
	for room, name := range (config.Display{Names: names}).RoomNames() {
		out[domain.RoomID(room)] = name
	}
	return out
}
