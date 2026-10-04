package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// starterRail is a first-run rail with every starter tag holding something, plus one
// space.
func starterRail(t *testing.T) Model {
	t.Helper()
	m := starterNew(apitest.Nop{}, config.Display{})
	cfg := m.conf.base.Clone()
	for i := range cfg.Tags {
		if cfg.Tags[i].Name == "Archived" {
			cfg.Tags[i].Picked = []string{"!old:x"}
		}
	}
	m = m.WithConfigFile("", cfg)
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!r:x", Name: "Room"}, {ID: "!old:x", Name: "Old"},
		{ID: "!i:x", Name: "Invited", Membership: domain.MembershipInvite},
	}})
	return update(t, m, spacesMsg{spaces: []domain.Space{{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!r:x"}}}})
}

// Every rail key is one of three kinds, and everything that asks agrees which: a
// space's (its name), a tag's (tag:<name>), or the fallback row of a rail with
// neither.
func TestEveryRailKeyIsClassified(t *testing.T) {
	t.Parallel()

	m := starterRail(t)
	for _, g := range m.rail.groups {
		_, tag := domain.TagOf(g.key)
		switch {
		case g.key == "Work":
			if !isSpaceGroup(g.key) || isTagGroup(g.key) {
				t.Errorf("%q is a space and is not classified as one", g.key)
			}
		case tag:
			if isSpaceGroup(g.key) || !isTagGroup(g.key) {
				t.Errorf("%q is a tag and is not classified as one", g.key)
			}
			if _, ok := m.rail.tags.Index(strings.TrimPrefix(g.key, "tag:")); !ok {
				t.Errorf("rail row %q names no configured tag", g.key)
			}
		default:
			t.Errorf("rail row %q is neither a space nor a tag", g.key)
		}
	}
	if isSpaceGroup(fallbackGroupKey) || isTagGroup(fallbackGroupKey) {
		t.Errorf("the fallback row %q is classified as a space or a tag", fallbackGroupKey)
	}
}

// Every row's label is the one the rail shows, also asked for by key (a hidden row
// once offered itself back by its key).
func TestEveryRowHasItsLabel(t *testing.T) {
	t.Parallel()

	m := starterRail(t)
	for _, g := range m.rail.groups {
		want := g.label
		if g.countInLabel {
			continue // the count is the row's, not the name's
		}
		if got := m.groupLabelFor(g.key); got != want {
			t.Errorf("groupLabelFor(%q) = %q, want %q", g.key, got, want)
		}
		if strings.HasPrefix(g.label, "tag:") {
			t.Errorf("row %q labels itself %q — the key leaking into the UI", g.key, g.label)
		}
	}
	if got := (Model{}).groupLabelFor(fallbackGroupKey); got != "All" {
		t.Errorf("the fallback row's label = %q, want All", got)
	}
}

// With no spaces and no tags the rail is one row of every room, and it is not a
// place: no name rule or notification rule is written against it.
func TestTheFallbackRowRefusesPlaceRules(t *testing.T) {
	t.Parallel()

	m := withRooms(t, New(t.Context(), apitest.Nop{}, config.Display{}))
	if keys := groupKeys(m.rail.groups); len(keys) != 1 || keys[0] != fallbackGroupKey {
		t.Fatalf("rail = %v, want the fallback row alone", keys)
	}
	if !admitsRoom(m, m.rail.groups[0], "!a:x") || !admitsRoom(m, m.rail.groups[0], "!b:x") {
		t.Error("the fallback row does not hold every room")
	}
	m.rail.cursor = 0
	afterRule, _ := m.openRuleForGroup()
	if len(afterRule.aimedAt.ruleScopes) != 0 {
		t.Error("aimed a notification rule at a row that is not a place")
	}
}
