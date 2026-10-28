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

// configured builds a model as the program does: display first, then the rest.
func configured(cfg config.Config) Model {
	return New(context.Background(), apitest.Nop{}, cfg.Display).WithConfigFile("", asStarted(cfg))
}

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

// Each unhidden tag is a rail row holding what its rule, picks and exclusions say; a
// state word follows the state with nothing rebuilt.
func TestTagRowsHoldWhatTheirRulesSay(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, config.Config{Tags: []config.Tag{
		{Name: "Family", Rule: []string{"dm"}},
		{Name: "Picked", Picked: []string{mx + "!a:x"}},
		{Name: "Busy", Rule: []string{"unread"}},
		{Name: "Secret", Rule: []string{"*"}, Hidden: true},
		{Name: "NotBravo", Rule: []string{"*"}, Excluded: []string{"room:Bravo"}},
	}})
	keys := groupKeys(m.rail.groups)
	if slices.Contains(keys, "tag:Secret") {
		t.Errorf("a hidden tag has a rail row: %v", keys)
	}
	family, picked, busy, notBravo := railRow(t, m, "tag:Family"), railRow(t, m, "tag:Picked"), railRow(t, m, "tag:Busy"), railRow(t, m, "tag:NotBravo")
	if !admitsRoom(m, family, mx+"!b:x") || admitsRoom(m, family, mx+"!a:x") {
		t.Error("Family does not hold the DM alone")
	}
	if !admitsRoom(m, picked, mx+"!a:x") || admitsRoom(m, picked, mx+"!b:x") {
		t.Error("Picked does not hold the picked room alone")
	}
	if !admitsRoom(m, notBravo, mx+"!a:x") || admitsRoom(m, notBravo, mx+"!b:x") {
		t.Error("an excluded room is still held")
	}
	if admitsRoom(m, busy, mx+"!a:x") {
		t.Fatal("Busy holds a read room")
	}
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: mx + "!a:x", Messages: 2, Counted: true}})
	if !admitsRoom(m, busy, mx+"!a:x") {
		t.Error("Busy does not follow a room becoming unread")
	}
	if g := railRow(t, m, "tag:Family"); g.label != "Family" {
		t.Errorf("label = %q, want the tag's name", g.label)
	}
}

func TestTagRowsTakeTheRailOrder(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Rail.Order = []string{"tag:Family", "*"}
	m := taggedModel(t, cfg)
	if got := m.rail.groups[0].key; got != "tag:Family" {
		t.Errorf("first row = %q, want the tag the order names first", got)
	}
}

// Interdependent tag rules are reported on the status line at once; a tag naming
// nothing is refused and the running config kept.
func TestTagProblemsAreSaid(t *testing.T) {
	t.Parallel()
	m := configured(config.Config{Tags: []config.Tag{
		{Name: "A", Rule: []string{"tag:B"}},
		{Name: "B", Rule: []string{"tag:A"}},
	}})
	if got := m.status(); !strings.Contains(got, "A, B") {
		t.Errorf("status = %q, want the cycle named", got)
	}

	bad := m.conf.base.Clone()
	bad.Tags = append(bad.Tags, config.Tag{Name: "C", Rule: []string{"unred"}})
	next, _ := m.applyConfig(bad, "applied")
	if _, ok := findGroup(next.rail.groups, "tag:C"); ok {
		t.Error("a refused tag reached the rail")
	}
}

// A tag row is a home, not a space: it can't be renamed from the rail (everything
// refers to it by name), but name and notification rules can target it as tag:<name>.
func TestATagRowIsAHomeNotASpace(t *testing.T) {
	t.Parallel()
	if isSpaceGroup("tag:Family") || !isTagGroup("tag:Family") || isTagGroup("Work") || !isSpaceGroup("Work") || isSpaceGroup(fallbackGroupKey) {
		t.Fatal("rail keys misclassified")
	}
	m := taggedModel(t, config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "tag:Family")
	next, _ := m.renameGroup()
	if next.aimedAt.renamingGroup != "" || !strings.Contains(next.status(), "[[tag]]") {
		t.Errorf("renaming a tag row: renaming %q, status %q; want it refused, pointing at [[tag]]", next.aimedAt.renamingGroup, next.status())
	}
	ruled, _ := m.openRuleForGroup()
	if len(ruled.aimedAt.ruleScopes) != 1 || ruled.aimedAt.ruleScopes[0].match != "tag:Family" {
		t.Errorf("a rule on a tag row: scopes %+v, want tag:Family", ruled.aimedAt.ruleScopes)
	}
}
