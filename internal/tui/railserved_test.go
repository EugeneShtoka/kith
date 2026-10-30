package tui

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// twoProfiles is a window over b with home and work, started in current ("" none), its
// device file at a temporary path.
func twoProfiles(t *testing.T, b *profiledBackend, current string) Model {
	t.Helper()
	cfg := config.Config{Profiles: []config.Profile{{Name: "home", Include: []string{"*"}}, {Name: "work", Include: []string{"*"}}}}
	m := starterNew(b, config.Display{}).WithDevice(filepath.Join(t.TempDir(), config.DeviceFile), config.Device{})
	return m.WithConfigFile("", cfg).WithProfiles(b, current)
}

// rememberedIn is the profile the device file says the window runs in.
func rememberedIn(t *testing.T, m Model) string {
	t.Helper()
	dev, err := config.LoadDevice(m.conf.deviceFile.path)
	if err != nil {
		t.Fatal(err)
	}
	return dev.ProfileInUse
}

// Started in no profile (no --profile, none remembered) with several in the config, the
// window offers them; the pick runs it there and is kept in the device file. Started in
// one, or with one profile, it offers nothing.
func TestAStartInNoProfileOffersThemAndKeepsThePick(t *testing.T) {
	t.Parallel()
	b := &profiledBackend{rooms: map[string][]domain.Room{"": nil, "home": nil, "work": nil}}
	m := twoProfiles(t, b, "")
	if m.picker.kind != pickerProfile || len(m.picker.items) != 2 {
		t.Fatalf("picker = %v %+v, want both profiles offered", m.picker.kind, m.picker.items)
	}
	m, cmd := m.switchProfile("work")
	m = settle(t, m, cmd)
	if !slices.Equal(b.switches, []string{"work"}) || m.profiles.current != "work" {
		t.Fatalf("switched to %v, running in %q; want work", b.switches, m.profiles.current)
	}
	if got := rememberedIn(t, m); got != "work" {
		t.Errorf("profile_in_use = %q, want work kept", got)
	}

	// Picking the profile it already runs in keeps that.
	first := twoProfiles(t, &profiledBackend{}, "")
	first, cmd = first.switchProfile("home")
	first = settle(t, first, cmd)
	if got := rememberedIn(t, first); got != "home" {
		t.Errorf("picking the profile running: profile_in_use = %q, want home", got)
	}

	if started := twoProfiles(t, &profiledBackend{}, "work"); started.picker.kind == pickerProfile {
		t.Error("a window started in a profile (remembered, or --profile) offered the profiles")
	}
	one := starterNew(&profiledBackend{}, config.Display{}).
		WithConfigFile("", config.Config{Profiles: []config.Profile{{Name: "only", Include: []string{"*"}}}}).
		WithProfiles(&profiledBackend{}, "")
	if one.picker.kind == pickerProfile {
		t.Error("a config with one profile offered it")
	}
}

// A profile a reload removed is refused its rail: the window draws no rail and offers
// the profiles there are; picking one runs the window there and keeps it.
func TestAWindowWhoseProfileIsGoneDrawsNoRailAndOffersTheRest(t *testing.T) {
	t.Parallel()
	b := &profiledBackend{rooms: map[string][]domain.Room{"work": {{ID: mx + "!w:x", Name: "Standup"}}}}
	m := sized(t, twoProfiles(t, b, "work"))
	m = update(t, m, roomsMsg{rooms: b.rooms["work"]})
	if len(m.rail.groups) == 0 {
		t.Fatal("no rail before the profile went")
	}

	gone := config.Config{Profiles: []config.Profile{{Name: "home", Include: []string{"*"}}}}
	m, _ = m.adoptConfig(gone, "")
	m, _ = m.handleRail(railMsg{profile: "work", err: api.ErrUnknownProfile})
	if len(m.rail.groups) != 0 || len(m.filteredRooms()) != 0 {
		t.Errorf("rail %v, rooms %v; want none in a profile that is gone", groupKeys(m.rail.groups), m.filteredRooms())
	}
	if m.picker.kind != pickerProfile || len(m.picker.items) != 1 || m.picker.items[0].value != "home" {
		t.Fatalf("picker = %v %+v, want home offered", m.picker.kind, m.picker.items)
	}
	m, cmd := m.switchProfile("home")
	m = settle(t, m, cmd)
	if m.profiles.current != "home" || rememberedIn(t, m) != "home" || len(m.rail.groups) == 0 {
		t.Errorf("after the pick: in %q, kept %q, rail %v; want home, drawn", m.profiles.current, rememberedIn(t, m), groupKeys(m.rail.groups))
	}
}

// A rail asked in a profile the window has left since is dropped, whatever its revision.
func TestARailForAProfileLeftIsDropped(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	held := m.rail.served
	stray := domain.ServedRail{Revision: held.rail.Revision + 10, Rows: []domain.RailRow{{Key: "tag:Elsewhere"}}}
	next, _ := m.handleRail(railMsg{profile: "another", rail: stray})
	if !sameServed(next.rail.served.rail, held.rail) || next.rail.served.rail.Revision != held.rail.Revision {
		t.Errorf("a rail of another profile replaced this window's: %+v", next.rail.served.rail)
	}
}

// A chat a link opened, which the rail does not know yet, stays listed in the row the
// cursor is on while it is open, whichever row that is; once the rail knows it, its
// rows decide.
func TestAChatTheRailDoesNotKnowStaysListedWhileOpen(t *testing.T) {
	t.Parallel()
	m := sized(t, withRooms(t, newModel()))
	fresh := domain.Room{ID: "telegram:1/200", Name: "Fresh", IsDirect: true}
	m = update(t, m, linkResolvedMsg{uri: "tg://resolve?domain=fresh", target: domain.LinkTarget{Room: fresh}})
	if m.openRoom != fresh.ID {
		t.Fatalf("open room = %q, want the fresh chat", m.openRoom)
	}
	if _, known := m.rail.served.room(fresh.ID); known {
		t.Fatal("the rail served knows a chat no listing holds")
	}
	for i := range m.rail.groups {
		m.rail.cursor = i
		if !slices.ContainsFunc(m.filteredRooms(), func(r domain.Room) bool { return r.ID == fresh.ID }) {
			t.Errorf("row %q does not list the open chat the rail does not know", m.rail.groups[i].key)
		}
	}
}

// The cursor stays on the row it was on across rails, by its key and space: two spaces
// of one name are two rows. A row that hides while empty and empties under the cursor
// stays, empty, until the cursor leaves it; any other row gone hands the cursor to the
// row in its place.
func TestTheRailCursorFollowsItsRowAcrossRails(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	twin := func(key string, space domain.SpaceID, rooms ...domain.RoomID) domain.RailRow {
		return domain.RailRow{Key: key, Label: key, Space: space, Rooms: rooms}
	}
	serve := func(m Model, rows ...domain.RailRow) Model {
		next, _ := m.handleRail(railMsg{profile: m.profiles.current, rail: domain.ServedRail{
			Revision: m.rail.served.rail.Revision + 1, Rows: rows, Rooms: m.rail.served.rail.Rooms,
		}})
		return next
	}
	a, b := domain.RoomID(mx+"!a:x"), domain.RoomID(mx+"!b:x")
	unread := domain.RailRow{Key: "tag:Unread", Label: "Unread", HideWhenEmpty: true, Rooms: []domain.RoomID{a}}

	m = serve(m, twin("Work", mx+"!w1:x", a), twin("Work", mx+"!w2:x", b), unread)
	m.rail.cursor = 1
	m = serve(m, unread, twin("Work", mx+"!w1:x", a), twin("Work", mx+"!w2:x", b))
	if g, _ := m.rail.at(); g.space != mx+"!w2:x" {
		t.Errorf("after the rows moved the cursor is on %q in %s, want the second Work", g.key, g.space)
	}

	m.rail.cursor = 0
	m = serve(m, twin("Work", mx+"!w1:x", a), twin("Work", mx+"!w2:x", b))
	if g, _ := m.rail.at(); g.key != "tag:Unread" || len(m.rail.groups) != 3 || len(m.filteredRooms()) != 0 {
		t.Errorf("the emptied Unread under the cursor: on %q of %v, listing %v; want it kept, empty", g.key, groupKeys(m.rail.groups), m.filteredRooms())
	}
	m.rail.cursor = 1
	m = serve(m, twin("Work", mx+"!w1:x", a), twin("Work", mx+"!w2:x", b))
	if slices.ContainsFunc(m.rail.groups, func(g group) bool { return g.key == "tag:Unread" }) {
		t.Errorf("Unread kept after the cursor left it: %v", groupKeys(m.rail.groups))
	}

	m.rail.cursor = 1
	m = serve(m, twin("Work", mx+"!w1:x", a), twin("Friends", mx+"!f:x", b))
	if g, _ := m.rail.at(); g.key != "Friends" {
		t.Errorf("the row under the cursor gone: the cursor is on %q, want the row now in its place", g.key)
	}
}
