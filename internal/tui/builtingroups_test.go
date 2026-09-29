package tui

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// railWithEveryBuiltIn builds a rail with every built-in row (the conditional ones only
// appear when non-empty) plus one real space.
func railWithEveryBuiltIn(t *testing.T) []group {
	t.Helper()

	const room = domain.RoomID("!r:x")
	view := unreadView{
		archived:      domain.Archive{Entries: []string{string(room)}},
		archivedRooms: map[domain.RoomID]bool{room: true},
		pinned:        domain.Pinned{Entries: []string{string(room)}},
		spamRooms:     map[domain.RoomID]bool{room: true},
		facts:         func(r domain.Room) domain.RoomFacts { return domain.RoomFacts{ID: string(r.ID)} },
	}
	drafts := map[domain.RoomID]draft{room: {}}
	spaces := []domain.Space{{ID: "!w:x", Name: "Work"}}

	groups := railGroups(spaces, config.Rail{}, nil, view, 1, nil, drafts)
	if len(groups) < len(builtInKeys(t)) {
		t.Fatalf("rail has %d groups, want at least the %d built-in rows: %v",
			len(groups), len(builtInKeys(t)), groupKeys(groups))
	}
	return groups
}

// Every rail key is classified the same way by everything that asks.
func TestEveryRailKeyIsClassified(t *testing.T) {
	t.Parallel()

	groups := railWithEveryBuiltIn(t)
	spaceKeys := map[string]bool{"Work": true}

	for _, g := range groups {
		if g.sepAfter && g.key == "" {
			continue
		}
		builtIn := config.BuiltInGroup(g.key)
		if spaceKeys[g.key] {
			if builtIn {
				t.Errorf("%q is a Matrix space and BuiltInGroup says otherwise", g.key)
			}
			continue
		}
		if !builtIn {
			t.Errorf("rail row %q is one of ours and no list knows it — "+
				"a rule or a name aimed at it would be written as if it were a space", g.key)
			continue
		}
		if !config.BuiltInGroup(g.key) {
			t.Errorf("config.BuiltInGroup(%q) = false: first_name_only and notification rules would "+
				"be written against a space that does not exist", g.key)
		}
		if got, want := config.GroupTarget(g.key), config.NameTargetGroup+g.key; got != want {
			t.Errorf("config.GroupTarget(%q) = %q, want %q — a rename written under the wrong "+
				"prefix collides with a real space of that name", g.key, got, want)
		}
	}
}

// Every built-in row has a label, and it is the label the rail shows (a hidden Drafts
// row once offered itself back as "drafts").
func TestEveryBuiltInRowHasALabel(t *testing.T) {
	t.Parallel()

	m := Model{}
	for _, key := range builtInKeys(t) {
		label, ok := builtInLabels[key]
		if !ok || label == "" {
			t.Errorf("built-in row %q has no label", key)
			continue
		}
		if got := m.groupLabelFor(key); got != label {
			t.Errorf("groupLabelFor(%q) = %q, want %q", key, got, label)
		}
		if label == key {
			t.Errorf("row %q labels itself %q — that is the key leaking into the UI", key, label)
		}
	}

	for _, g := range railWithEveryBuiltIn(t) {
		want, ok := builtInLabels[g.key]
		if !ok {
			continue
		}
		if g.key == inviteGroupKey {
			if !strings.HasPrefix(g.label, want) {
				t.Errorf("invites label %q does not start with %q", g.label, want)
			}
			continue
		}
		if g.label != want {
			t.Errorf("rail row %q is labeled %q, the table says %q", g.key, g.label, want)
		}
	}
}

// A rule aimed at a built-in row is refused, and the refusal names the row.
func TestBuiltInRowsRefuseSpaceRules(t *testing.T) {
	t.Parallel()

	for _, key := range builtInKeys(t) {
		m := Model{}
		m.rail.groups = []group{{key: key, label: builtInLabels[key]}}
		m.rail.cursor = 0

		after, _ := m.toggleFirstNameOnly()
		if len(after.prefs.display.SpaceRules) != 0 {
			t.Errorf("%q: wrote %d space rule(s) for a row that is not a space",
				key, len(after.prefs.display.SpaceRules))
		}
		if !strings.Contains(after.status(), "not to") {
			t.Errorf("%q: said %q, want a refusal naming the row", key, after.status())
		}

		afterRule, _ := m.openRuleForGroup()
		if len(afterRule.aimedAt.ruleScopes) != 0 {
			t.Errorf("%q: aimed a notification rule at a row that is not a place", key)
		}
	}
}

// builtInKeys is the rail's own rows, sorted, as this package labels them; each must be
// one config calls built in, or the two disagree about what a space is.
func builtInKeys(t *testing.T) []string {
	t.Helper()
	keys := slices.Sorted(maps.Keys(builtInLabels))
	for _, key := range keys {
		if !config.BuiltInGroup(key) {
			t.Fatalf("%q has a built-in label but config does not call it built in", key)
		}
	}
	return keys
}
