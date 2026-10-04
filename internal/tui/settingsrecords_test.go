package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every record table has a row in settings, but the space rules, which the Names
// group's switches stand for.
func TestEveryRecordTableHasARow(t *testing.T) {
	t.Parallel()
	for _, tb := range config.RecordTables() {
		if tb.Path == "display.space_rule" {
			continue
		}
		if _, ok := findSetting(recordPrefix + tb.Path); !ok {
			t.Errorf("%s has no row", tb.Path)
		}
	}
}

// Names has a first-names switch for every space and tag; switching one writes its
// rule (a tag's as tag:<name>), switching it back drops it, and the tag editor's
// switch is the same rule.
func TestFirstNamesPerPlace(t *testing.T) {
	t.Parallel()
	m, path := laidOut(t, config.Display{})
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}, {ID: "!d:x", Name: "Dana", IsDirect: true},
	}})
	m = m.settingsIn("names", "")
	for _, label := range []string{"First names only in Work", "First names only in Friends", "First names only in DMs"} {
		if got := rowFor(t, m, label).detail; got != "off" {
			t.Errorf("%s reads %q, want off", label, got)
		}
	}
	// Unread is state, nobody's home; All is every room.
	for _, item := range m.picker.items {
		if item.label == "First names only in Unread" || item.label == "First names only in All" {
			t.Errorf("%q is offered, but cannot be a room's home", item.label)
		}
	}
	m = pickLabel(t, m, "only in Work")
	m = pickLabel(t, m, "only in DMs")
	rules := m.conf.base.Display.SpaceRules
	if !slices.Contains(rules, config.SpaceRule{Space: "Work", FirstNameOnly: true}) ||
		!slices.Contains(rules, config.SpaceRule{Space: "tag:DMs", FirstNameOnly: true}) {
		t.Fatalf("rules = %+v, want Work and tag:DMs on", rules)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Display.SpaceRules) != 2 {
		t.Errorf("saved rules = %+v", reloaded.Display.SpaceRules)
	}
	m = pickLabel(t, m, "only in Work")
	if slices.ContainsFunc(m.conf.base.Display.SpaceRules, func(r config.SpaceRule) bool { return r.Space == "Work" }) {
		t.Error("switched off, Work's rule is still written")
	}

	// The tag editor shows and switches the same rule.
	m = m.tagOpen("DMs")
	if got := rowFor(t, m, "First names only").detail; got != "on" {
		t.Errorf("the tag editor reads %q for DMs, want on (set from Names)", got)
	}
	m = pickLabel(t, m, "First names only")
	if slices.ContainsFunc(m.conf.base.Display.SpaceRules, func(r config.SpaceRule) bool { return r.Space == "tag:DMs" }) {
		t.Error("the tag editor's switch did not turn DMs' rule off")
	}
}

// A record is added typed as its first field, its fields set as any setting is (a
// number stepped, an on/off/unset choice, a list an entry at a time), and removed;
// each change saved; esc walks back record → table → group → groups.
func TestRecordsAreEditedInSettings(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn("display", recordPrefix+"display.read_rule")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picker.kind != pickerSetting || m.choosing.settingGroup != recordPrefix+"display.read_rule" {
		t.Fatalf("Read rules opened %v on %q", m.picker.kind, m.choosing.settingGroup)
	}
	m = pickLabel(t, m, "Add one")
	m = typeIn(t, m, "Work")
	if got := m.conf.base.Display.ReadRules; len(got) != 1 || got[0].Match != "Work" {
		t.Fatalf("after adding: %+v, want one rule matching Work", got)
	}
	m = pickLabel(t, m, "Work") // the record
	if m.choosing.settingGroup != recordPrefix+"display.read_rule#0" {
		t.Fatalf("the record opened %q", m.choosing.settingGroup)
	}
	m = m.settingsIn(m.choosing.settingGroup, recordPrefix+"display.read_rule#0.delay")
	for range 5 {
		m, _ = press(t, m, keyText("+"))
	}
	m = pickLabel(t, m, "Send")
	m = pickLabel(t, m, "off")
	rule := m.conf.base.Display.ReadRules[0]
	if rule.Delay == nil || *rule.Delay != 5 || rule.Send == nil || *rule.Send {
		t.Errorf("rule = %+v, want delay 5 and send off", rule)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := reloaded.Display.ReadRules; len(r) != 1 || r[0].Delay == nil || *r[0].Delay != 5 {
		t.Errorf("saved %+v", r)
	}

	m = pickLabel(t, m, "Remove this one")
	if len(m.conf.base.Display.ReadRules) != 0 || m.choosing.settingGroup != recordPrefix+"display.read_rule" {
		t.Errorf("after removing: %+v, in %q", m.conf.base.Display.ReadRules, m.choosing.settingGroup)
	}

	// A list field: a person's IDs, an entry at a time.
	m = m.settingsIn("names", recordPrefix+"display.identity")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pickLabel(t, m, "Add one")
	m = typeIn(t, m, "Dana")
	m = pickLabel(t, m, "Dana")
	m = m.settingsIn(m.choosing.settingGroup, recordPrefix+"display.identity#0.ids")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pickLabel(t, m, "Add an entry")
	m = typeIn(t, m, "@dana:x")
	if ids := m.conf.base.Display.Identities[0].IDs; !slices.Equal(ids, []string{"@dana:x"}) {
		t.Errorf("ids = %v", ids)
	}

	esc := func(m Model) Model { m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}); return m }
	m = esc(m) // the list → the record
	if m.choosing.settingGroup != recordPrefix+"display.identity#0" {
		t.Errorf("esc from the ids: in %q, want the record", m.choosing.settingGroup)
	}
	m = esc(m)
	if m.choosing.settingGroup != recordPrefix+"display.identity" {
		t.Errorf("esc from the record: in %q, want the table", m.choosing.settingGroup)
	}
	m = esc(m)
	if m.choosing.settingGroup != "names" {
		t.Errorf("esc from the table: in %q, want Names", m.choosing.settingGroup)
	}
	if m = esc(m); m.picker.kind != pickerSettingGroups {
		t.Errorf("esc from Names: picker %v, want the groups", m.picker.kind)
	}
}
