package tui

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The tests play kithd: after each Update (asModel) the window is served the rail kithd
// would build from the window's own state, as CHANGE_RAIL and Rail bring it.

// kithdRail is the rail kithd would serve the window's profile now, built as kithd builds
// it (domain.BuildRail, Rail.Served) from the window's rooms, spaces, unread, drafts, tags
// and rail settings. kithd knows nothing of a chat a link opened, nor of parents the
// window never asked for: parents are the canonical parents kithd found.
func kithdRail(m Model, parents map[domain.RoomID]domain.SpaceID) domain.ServedRail {
	spec := setup.RailSpec(m.rail.tags, m.rail.cfg, m.prefs.display.Names)
	view := m.unreadView()
	rooms := make([]domain.RailRoom, 0, len(m.rooms.all))
	for i := range m.rooms.all {
		room := m.rooms.all[i]
		if slices.ContainsFunc(m.rooms.fresh, func(r domain.Room) bool { return r.ID == room.ID }) {
			continue
		}
		rooms = append(rooms, domain.RailRoom{ID: room.ID, Facts: m.factsFor(room), State: view.rawState(room), Parent: parents[room.ID]})
	}
	return domain.BuildRail(spec, m.rooms.spaces, rooms).Served(spec, rooms)
}

// serveRail hands the window the rail kithd would serve now when it differs from the one
// it holds, under the next revision; a profile the config lacks is refused, once.
func serveRail(m Model) Model {
	held := m.rail.served
	if current := m.profiles.current; current != "" && !slices.ContainsFunc(m.profileNames(), func(n string) bool { return strings.EqualFold(n, current) }) {
		if held.rooms == nil && len(m.rail.groups) == 0 {
			return m
		}
		next, _ := m.handleRail(railMsg{profile: current, err: api.ErrUnknownProfile})
		return next
	}
	// Taking a rail can change what the next is built from (it opens the top room, which
	// reads it), as kithd hears and builds again.
	for range 3 {
		next := servedWith(m, kithdRail(m, nil))
		if next.rail.served.rail.Revision == m.rail.served.rail.Revision {
			return next
		}
		m = next
	}
	return m
}

// servedWith hands the window want, unless it holds it already.
func servedWith(m Model, want domain.ServedRail) Model {
	held := m.rail.served
	if strings.EqualFold(held.profile, m.profiles.current) && held.rooms != nil && sameServed(held.rail, want) {
		return m
	}
	want.Revision = held.rail.Revision + 1
	next, _ := m.handleRail(railMsg{profile: m.profiles.current, rail: want})
	return next
}

// sameServed reports whether two rails hold the same rows and rooms.
func sameServed(a, b domain.ServedRail) bool {
	a.Revision, b.Revision = 0, 0
	return reflect.DeepEqual(a, b)
}

// staleRail returns how the rail the window draws differs from the rail served, or nil:
// the rows drawn are the rows served (one that hides while empty may stay under the
// cursor), and the index of each row's rooms and of the rooms is the rail's.
func staleRail(m Model) error {
	served := m.rail.served
	fresh := newServedRail(served.profile, served.rail)
	if !maps.EqualFunc(served.holds, fresh.holds, maps.Equal) || !maps.Equal(served.rooms, fresh.rooms) {
		return fmt.Errorf("the rail's index is %v, %v; the rail served indexes as %v, %v", served.holds, served.rooms, fresh.holds, fresh.rooms)
	}
	want := m.railFromState()
	got := m.rail.groups
	if len(got) == len(want)+1 && m.rail.cursor < len(got) {
		kept := got[m.rail.cursor]
		if slices.ContainsFunc(want, kept.sameRow) || (!kept.hidesEmpty && !slices.ContainsFunc(m.rail.cfg.HideWhenEmpty, kept.namedBy)) {
			return fmt.Errorf("the rail keeps %q under the cursor, which is no row emptied while there", kept.key)
		}
		got = slices.Delete(slices.Clone(got), m.rail.cursor, m.rail.cursor+1)
	}
	if !slices.EqualFunc(got, want, func(a, b group) bool { return a == b }) {
		return fmt.Errorf("the rail draws\n%+v\nbut the rail served is\n%+v", m.rail.groups, want)
	}
	return nil
}

// railGroups is the rows kithd would serve for these inputs, as groups: the rail built as
// kithd builds it, each room judged with view's tags, facts and state.
func railGroups(spaces []domain.Space, cfg config.Rail, names []config.DisplayName, view unreadView, rooms []domain.Room) []group {
	spec := setup.RailSpec(view.tags, cfg, names)
	in := make([]domain.RailRoom, len(rooms))
	for i := range rooms {
		in[i] = domain.RailRoom{ID: rooms[i].ID, Facts: view.factsOf(rooms[i]), State: view.rawState(rooms[i])}
	}
	return groupsOf(domain.BuildRail(spec, spaces, in).Served(spec, in).Rows)
}
