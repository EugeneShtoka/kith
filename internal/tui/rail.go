package tui

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
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
	// tagsRev counts tag applications, for tagMemo's key; tagMemo is shared by Model copies (see
	// tagged.go).
	tagsRev uint64
	tagMemo *tagMemo
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
