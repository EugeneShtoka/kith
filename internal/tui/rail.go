package tui

import (
	"github.com/EugeneShtoka/kith/internal/domain"
)

// railState is the space rail: its groups, the cursor, and everything that decides
// which rooms a group admits and how they sort. roomFacts and spamRooms are derived
// (see refreshPlaces); set their inputs without refreshing and they go stale.
type railState struct {
	groups []group
	cursor int
	rules  []domain.RoomListRule       // per-group room-list overrides
	chains map[string][]domain.SortKey // this session's sort changes, by group key
	// spam: [spam] rooms/except. caught: rooms the daemon's filters promoted.
	spam      domain.Spam
	caught    map[domain.RoomID]domain.SpamVerdict
	spamRooms map[domain.RoomID]bool
	// tags: the [[tag]]s. roomFacts is every room's facts, precomputed (a tag is asked
	// about every room on every draw); spanning is the tags (as tag:<name>, lower-cased)
	// holding every room in it, derived with it.
	tags      domain.TagSet
	roomFacts map[domain.RoomID]domain.RoomFacts
	spanning  map[string]bool
	// tagsRev counts the tags applied, for tagMemo's key; tagMemo is shared by the
	// Model's copies (see tagged.go).
	tagsRev uint64
	tagMemo *tagMemo
}

// at is the group the cursor is on, bounds-checked because sync can rebuild the groups
// under the cursor.
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

// moved walks the cursor, stopping at the ends (no wrap).
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
