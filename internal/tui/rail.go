package tui

import (
	"reflect"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// railState is the space rail: groups, cursor, and what decides which rooms a group admits and
// how they sort. roomFacts is derived (see refreshPlaces); changing its inputs without
// refreshing leaves it stale.
type railState struct {
	groups []group
	cursor int
	rules  []domain.RoomListRule       // per-group room-list overrides
	chains map[string][]domain.SortKey // this session's sort changes, by group key
	// verdicts: what the daemon's filters moved each room into, and releases. caught: the
	// tags each room is in by a filter (domain.Places.Caught).
	verdicts map[domain.RoomID][]domain.TagVerdict
	caught   map[domain.RoomID][]string
	// people is what the filters put people into, and releases; peopleRev moves with it,
	// for the derived cache (a muted tag's people are hidden).
	people    []domain.PersonVerdict
	peopleRev uint64
	// tags: the [[tag]]s. roomFacts is every room's facts, precomputed since every draw asks a tag
	// about every room; spanning is the tags (tag:<name>, lower-cased) holding every room.
	tags      domain.TagSet
	roomFacts map[domain.RoomID]domain.RoomFacts
	// archives maps each followed network to its archive tag (domain.Places); mirrored is the
	// networks whose archive filing kith also changes.
	archives map[domain.Protocol]string
	mirrored map[domain.Protocol]string
	// archiveTags is every archive tag; archivedShown is the places whose archived toggle was
	// flipped, and to what, until restart (archived.go).
	archiveTags   []string
	archivedShown map[string]bool
	spanning      map[string]bool
	// homes is the order a room's homes are chosen in, derived with roomFacts (domain.HomeOrder).
	homes domain.HomeOrder
	// cfg is the rail drawn: the profile's own, or [display.rail] (derive).
	cfg config.Rail
	// tagsRev counts tag applications, for railMemo's key; railMemo is the rail built from the
	// model's state (railInputs), shared by Model copies.
	tagsRev  uint64
	railMemo *railMemo
}

// at is the group under the cursor, bounds-checked because sync can rebuild the groups.
func (r railState) at() (group, bool) {
	if r.cursor < 0 || r.cursor >= len(r.groups) {
		return group{}, false
	}
	return r.groups[r.cursor], true
}

// key is the current group's key, or "" when the cursor is on nothing.
func (r railState) key() string {
	g, ok := r.at()
	if !ok {
		return ""
	}
	return g.key
}

// label is the current group's label, or "" when the cursor is on nothing.
func (r railState) label() string {
	g, ok := r.at()
	if !ok {
		return ""
	}
	return g.label
}

// moved walks the cursor, stopping at the ends.
func (r railState) moved(delta int) (railState, bool) {
	next := r.cursor + delta
	if next < 0 || next >= len(r.groups) {
		return r, false
	}
	r.cursor = next
	return r, true
}

// went puts the cursor on the nth group, one-based and clamped (a count before gg/G).
func (r railState) went(n int) (railState, bool) {
	if len(r.groups) == 0 {
		return r, false
	}
	next := clampIndex(n-1, len(r.groups))
	if next == r.cursor {
		return r, false
	}
	r.cursor = next
	return r, true
}

// The rail as data: domain.BuildRail over the rooms in their state now, memoized since every
// draw asks each row about every room. The memo is keyed by its inputs' identities (cow.go:
// any change is a new map or slice) and holds those inputs, so a freed one's address cannot
// come back as an unchanged key.

// railInputs is what the rail is built from besides the view's tags, facts, unread, drafts and
// parents.
type railInputs struct {
	spaces []domain.Space
	rooms  []domain.Room
	cfg    config.Rail
	names  []config.DisplayName
}

// railRowRef names a rail row across builds: its key, and for a space's row the space (two
// spaces can share a name).
type railRowRef struct {
	key   string
	space domain.SpaceID
}

// builtRail is a rail and the spec it was built from.
type builtRail struct {
	spec domain.RailSpec
	rail domain.Rail
}

// railMemo is the last rail built and each row's rooms.
type railMemo struct {
	key   railKey
	held  unreadView // the inputs key names, kept alive
	ok    bool
	built builtRail
	holds map[railRowRef]map[domain.RoomID]bool
}

// railKey identifies the rail's inputs: the tags by revision, each map and slice by identity.
type railKey struct {
	tagsRev                        uint64
	local                          bool
	unread, drafts, facts, parents uintptr
	spaces, rooms, names           sliceRef
	order, hidden, hideWhenEmpty   sliceRef
}

// sliceRef is a slice's identity: where its elements start, and how many.
type sliceRef struct {
	at uintptr
	n  int
}

func sliceOf[T any](s []T) sliceRef {
	if len(s) == 0 {
		return sliceRef{}
	}
	return sliceRef{at: reflect.ValueOf(&s[0]).Pointer(), n: len(s)} // a pointer: boxing one does not allocate
}

func (v unreadView) railKey() railKey {
	return railKey{
		tagsRev: v.tagsRev, local: v.local,
		unread: identity(v.counts), drafts: identity(v.drafts), facts: identity(v.roomFacts), parents: identity(v.parents),
		spaces: sliceOf(v.rail.spaces), rooms: sliceOf(v.rail.rooms), names: sliceOf(v.rail.names),
		order: sliceOf(v.rail.cfg.Order), hidden: sliceOf(v.rail.cfg.Hidden), hideWhenEmpty: sliceOf(v.rail.cfg.HideWhenEmpty),
	}
}

// buildRail builds the rail from the view's state now, each room judged in its own.
func (v unreadView) buildRail() builtRail {
	spec := setup.RailSpec(v.tags, v.rail.cfg, v.rail.names)
	rooms := make([]domain.RailRoom, len(v.rail.rooms))
	for i := range v.rail.rooms {
		room := &v.rail.rooms[i]
		rooms[i] = domain.RailRoom{ID: room.ID, Facts: v.factsOf(*room), State: v.rawState(*room), Parent: v.parents[room.ID]}
	}
	return builtRail{spec: spec, rail: domain.BuildRail(spec, v.rail.spaces, rooms)}
}

// currentRail is the memo, made current for the view's inputs; a view without one builds
// afresh.
func (v unreadView) currentRail() *railMemo {
	memo := v.railMemo
	if memo == nil {
		memo = &railMemo{}
	}
	if key := v.railKey(); !memo.ok || memo.key != key {
		held := v
		held.facts, held.railMemo = nil, nil
		built := v.buildRail()
		holds := make(map[railRowRef]map[domain.RoomID]bool, len(built.rail.Rows))
		for i := range built.rail.Rows {
			row := &built.rail.Rows[i]
			in := make(map[domain.RoomID]bool, len(row.Rooms))
			for _, id := range row.Rooms {
				in[id] = true
			}
			holds[railRowRef{key: row.Key, space: row.Space}] = in
		}
		*memo = railMemo{key: key, held: held, ok: true, built: built, holds: holds}
	}
	return memo
}

// builtRail is the rail built from the view's state now.
func (v unreadView) builtRail() builtRail { return v.currentRail().built }

// railHolds reports whether the rail row named holds a room, in the rail built from the
// view's state now.
func (v unreadView) railHolds(row railRowRef, id domain.RoomID) bool {
	return v.currentRail().holds[row][id]
}
