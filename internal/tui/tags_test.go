package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// taggedModel is a model with tags configured and two rooms: Alpha, and a DM, Bravo.
func taggedModel(t *testing.T, cfg config.Config) Model {
	t.Helper()
	return withRooms(t, configured(cfg))
}

// configured is a model built as the program builds it: display first, then the rest.
func configured(cfg config.Config) Model {
	return New(context.Background(), apitest.Nop{}, cfg.Display).WithConfigFile("", cfg)
}

// railRow is the rail group with this key.
func railRow(t *testing.T, m Model, key string) group {
	t.Helper()
	g, ok := findGroup(m.rail.groups, key)
	if !ok {
		t.Fatalf("no rail row %q in %v", key, groupKeys(m.rail.groups))
	}
	return g
}

func admitsRoom(m Model, g group, id domain.RoomID) bool {
	for i := range m.rooms.all {
		if m.rooms.all[i].ID == id {
			return g.admits(m.unreadView(), m.rooms.all[i])
		}
	}
	return false
}

// Each tag not hidden is a rail row, holding what its rule, picks and exclusions say;
// a state word follows the state as it changes, with nothing rebuilt.
func TestTagRowsHoldWhatTheirRulesSay(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, config.Config{Tags: []config.Tag{
		{Name: "Family", Rule: []string{"dm"}},
		{Name: "Picked", Picked: []string{"!a:x"}},
		{Name: "Busy", Rule: []string{"unread"}},
		{Name: "Secret", Rule: []string{"*"}, Hidden: true},
		{Name: "NotBravo", Rule: []string{"*"}, Excluded: []string{"room:Bravo"}},
	}})
	keys := groupKeys(m.rail.groups)
	if slices.Contains(keys, "tag:Secret") {
		t.Errorf("a hidden tag has a rail row: %v", keys)
	}
	family, picked, busy, notBravo := railRow(t, m, "tag:Family"), railRow(t, m, "tag:Picked"), railRow(t, m, "tag:Busy"), railRow(t, m, "tag:NotBravo")
	if !admitsRoom(m, family, "!b:x") || admitsRoom(m, family, "!a:x") {
		t.Error("Family does not hold the DM alone")
	}
	if !admitsRoom(m, picked, "!a:x") || admitsRoom(m, picked, "!b:x") {
		t.Error("Picked does not hold the picked room alone")
	}
	if !admitsRoom(m, notBravo, "!a:x") || admitsRoom(m, notBravo, "!b:x") {
		t.Error("an excluded room is still held")
	}
	if admitsRoom(m, busy, "!a:x") {
		t.Fatal("Busy holds a read room")
	}
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Messages: 2, Counted: true}})
	if !admitsRoom(m, busy, "!a:x") {
		t.Error("Busy does not follow a room becoming unread")
	}
	if g := railRow(t, m, "tag:Family"); g.label != "Family" {
		t.Errorf("label = %q, want the tag's name", g.label)
	}
}

// A tag is placed by the rail order like any row; an archived room leaves it, as it
// leaves every row but its space's.
func TestTagRowsTakeTheRailOrderAndLoseArchivedRooms(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Rail.Order = []string{"tag:Family", "*"}
	cfg.Display.Archived = []string{"!b:x"}
	m := taggedModel(t, cfg)
	if got := m.rail.groups[0].key; got != "tag:Family" {
		t.Errorf("first row = %q, want the tag the order names first", got)
	}
	if admitsRoom(m, railRow(t, m, "tag:Family"), "!b:x") {
		t.Error("an archived room is still in a tag")
	}
}

// Tags whose rules depend on each other are said on the status line at once; a tag
// that names nothing is refused, the running config kept.
func TestTagProblemsAreSaid(t *testing.T) {
	t.Parallel()
	m := configured(config.Config{Tags: []config.Tag{
		{Name: "A", Rule: []string{"tag:B"}},
		{Name: "B", Rule: []string{"tag:A"}},
	}})
	if got := m.status(); !strings.Contains(got, "depend on each other") || !strings.Contains(got, "A, B") {
		t.Errorf("status = %q, want the cycle named", got)
	}

	bad := m.conf.base.Clone()
	bad.Tags = append(bad.Tags, config.Tag{Name: "C", Rule: []string{"unred"}})
	next, _ := m.applyConfig(bad, "", "applied")
	if got := next.status(); !strings.Contains(got, "could not apply") {
		t.Errorf("status = %q, want the bad tag refused", got)
	}
	if _, ok := findGroup(next.rail.groups, "tag:C"); ok {
		t.Error("a refused tag reached the rail")
	}
}

// A tag's row is not a space's: it is not renamed from the rail (its name is what
// everything refers to it by), and name and notification rules are not aimed at it.
func TestATagRowIsNotASpace(t *testing.T) {
	t.Parallel()
	if isSpaceGroup("tag:Family") || !isTagGroup("tag:Family") || isTagGroup("Work") || !isSpaceGroup("Work") || isSpaceGroup("home") {
		t.Fatal("rail keys misclassified")
	}
	m := taggedModel(t, config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "tag:Family")
	next, _ := m.renameGroup()
	if next.aimedAt.renamingGroup != "" || !strings.Contains(next.status(), "[[tag]]") {
		t.Errorf("renaming a tag row: renaming %q, status %q; want it refused, pointing at [[tag]]", next.aimedAt.renamingGroup, next.status())
	}
	for name, try := range map[string]func(Model) Model{
		"first names only": func(m Model) Model { n, _ := m.toggleFirstNameOnly(); return n },
		"a rule":           func(m Model) Model { n, _ := m.openRuleForGroup(); return n },
	} {
		if got := try(m).status(); !strings.Contains(got, "not to Family") {
			t.Errorf("%s on a tag row: status %q, want it refused", name, got)
		}
	}
}
