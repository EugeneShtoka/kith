package tui

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
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
	// about every room: the rail's where it serves the room, else built here; spanning is the
	// tags (tag:<name>, lower-cased) holding every room.
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
	// tagsRev counts tag applications, for the derived cache's key.
	tagsRev uint64
	// served is the rail kithd served this window (Rail): the rows drawn, and the rooms each
	// holds.
	served servedRail
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

// The rail is kithd's (Rail): it builds it for the profile this window runs in, from the
// config and the rooms in their state now, and says when it changes (CHANGE_RAIL). The
// window draws the rows served and asks a row about a room by looking it up, indexed
// once per rail since every draw asks every row about every room.

// Rails is the rail kithd builds for this window's profile. Declared here because only
// daemon.Remote implements it.
type Rails interface {
	Rail(ctx context.Context) (domain.ServedRail, error)
}

// railRowRef names a rail row across rails: its key, and for a space's row the space (two
// spaces can share a name).
type railRowRef struct {
	key   string
	space domain.SpaceID
}

// servedRail is the rail last served, the profile it was asked in, and its rows' rooms
// and its rooms indexed.
type servedRail struct {
	profile string
	rail    domain.ServedRail
	holds   map[railRowRef]map[domain.RoomID]bool
	rooms   map[domain.RoomID]int
}

// newServedRail indexes a rail served for profile.
func newServedRail(profile string, rail domain.ServedRail) servedRail {
	out := servedRail{
		profile: profile, rail: rail,
		holds: make(map[railRowRef]map[domain.RoomID]bool, len(rail.Rows)),
		rooms: make(map[domain.RoomID]int, len(rail.Rooms)),
	}
	for i := range rail.Rows {
		row := &rail.Rows[i]
		in := make(map[domain.RoomID]bool, len(row.Rooms))
		for _, id := range row.Rooms {
			in[id] = true
		}
		out.holds[railRowRef{key: row.Key, space: row.Space}] = in
	}
	for i := range rail.Rooms {
		out.rooms[domain.RoomID(rail.Rooms[i].Facts.ID)] = i
	}
	return out
}

// room is a room as the rail served judged it; false for one it does not know (a chat a
// link opened, or one newer than the rail).
func (s servedRail) room(id domain.RoomID) (domain.ServedRoom, bool) {
	i, ok := s.rooms[id]
	if !ok {
		return domain.ServedRoom{}, false
	}
	return s.rail.Rooms[i], true
}

// railMsg is the rail kithd served the profile it was asked in, or why it did not.
type railMsg struct {
	profile string
	rail    domain.ServedRail
	err     error
}

// loadRailCmd asks kithd for the rail of the profile this window runs in; nil for a
// backend that builds none.
func (m Model) loadRailCmd() tea.Cmd {
	source, ok := m.backend.(Rails)
	if !ok {
		return nil
	}
	ctx, profile := m.ctx, m.profiles.current
	return func() tea.Msg {
		rail, err := source.Rail(ctx)
		return railMsg{profile: profile, rail: rail, err: err}
	}
}

// handleRail draws the rail served, unless the window has left the profile it was asked
// in or holds a later one. A profile the config no longer has draws no rail, and the
// window offers the profiles there are.
func (m Model) handleRail(msg railMsg) (Model, tea.Cmd) {
	switch {
	case !strings.EqualFold(msg.profile, m.profiles.current):
		return m, nil
	case errors.Is(msg.err, api.ErrUnknownProfile):
		m.rail.served = servedRail{profile: msg.profile}
		m = m.refreshPlaces().rebuiltRail()
		return m.offerProfiles("profile " + m.currentProfile() + " is gone from the config — pick one"), nil
	case msg.err != nil:
		m.logErr(slog.LevelWarn, "read the rail", msg.err)
		return m, nil
	case strings.EqualFold(msg.profile, m.rail.served.profile) && msg.rail.Revision < m.rail.served.rail.Revision:
		return m, nil // an answer overtaken by a later one
	}
	// The cursor before the change, so the open room leaving the list (archived, filed
	// elsewhere) hands selection to a neighbor. One the list did not hold (opened from a
	// search), or held only while the rail did not know it, stays open.
	was := m.roomCursor()
	_, known := m.rail.served.room(m.openRoom)
	listed := known && slices.ContainsFunc(m.filteredRooms(), func(r domain.Room) bool { return r.ID == m.openRoom })
	m.rail.served = newServedRail(msg.profile, msg.rail)
	m = m.refreshPlaces().rebuiltRail()
	if m.openRoom != "" {
		if !listed {
			return m, nil
		}
		return m.keepCursorNearby(was)
	}
	// Nothing open yet (the rooms were listed before their rail): the top of the list, as
	// handleRooms lands there.
	if fr := m.filteredRooms(); len(fr) > 0 {
		return m.selectRoom(fr[0])
	}
	return m, nil
}

// railHolds reports whether the rail row named holds a room, in the rail served.
func (v unreadView) railHolds(row railRowRef, id domain.RoomID) bool {
	return v.served.holds[row][id]
}
