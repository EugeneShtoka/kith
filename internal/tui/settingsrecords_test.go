package tui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every record table has a row, except the space rules, which the Names switches stand for.
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

// Switching a place's first names writes its rule (a tag's as tag:<name>), switching
// back drops it, and the tag editor's switch is the same rule.
func TestFirstNamesPerPlace(t *testing.T) {
	t.Parallel()
	m, path := laidOut(t, config.Display{})
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!a:x", Name: "Alpha"}, {ID: mx + "!b:x", Name: "Bravo"}, {ID: mx + "!d:x", Name: "Dana", IsDirect: true},
	}})
	for _, key := range []string{"Work", "Friends", "tag:DMs"} {
		if got := rowFor(t, m.settingsIn(placePage(key), ""), "First names only").detail; got != "off" {
			t.Errorf("%s reads %q, want off", key, got)
		}
	}
	// Unread is state, nobody's home; All is every room.
	for _, key := range []string{"tag:Unread", "tag:All"} {
		for _, item := range m.settingsIn(placePage(key), "").picker.items {
			if item.label == "First names only" {
				t.Errorf("%s offers first names, but cannot be a room's home", key)
			}
		}
	}
	m = pickLabel(t, m.settingsIn(placePage("Work"), ""), "First names only")
	m = pickLabel(t, m.settingsIn(placePage("tag:DMs"), ""), "First names only")
	rules := m.conf.base.Display.SpaceRules
	if !slices.Contains(rules, config.SpaceRule{Space: "space:Work", FirstNameOnly: true}) ||
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
	m = pickLabel(t, m.settingsIn(placePage("Work"), ""), "First names only")
	if slices.ContainsFunc(m.conf.base.Display.SpaceRules, func(r config.SpaceRule) bool { return r.Space == "space:Work" }) {
		t.Error("switched off, Work's rule is still written")
	}
}

// A record is added as its first field, edited like any setting, and removed; each
// change is saved; esc walks back record → table → group → groups.
func TestRecordsAreEditedInSettings(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
	m = m.settingsIn("display", recordPrefix+"display.read_rule")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picker.kind != pickerSetting || m.choosing.settingGroup != recordPrefix+"display.read_rule" {
		t.Fatalf("Read rules opened %v on %q", m.picker.kind, m.choosing.settingGroup)
	}
	m = pickLabel(t, m, "Add one")
	m = typeIn(t, m, "space:Work")
	if got := m.conf.device.Display.ReadRules; len(got) != 1 || got[0].Match != "space:Work" {
		t.Fatalf("after adding: %+v, want one rule matching space:Work", got)
	}
	m = pickLabel(t, m, "space:Work") // the record
	if m.choosing.settingGroup != recordPrefix+"display.read_rule#0" {
		t.Fatalf("the record opened %q", m.choosing.settingGroup)
	}
	m = m.settingsIn(m.choosing.settingGroup, recordPrefix+"display.read_rule#0.delay")
	for range 5 {
		m, _ = press(t, m, keyText("+"))
	}
	m = pickLabel(t, m, "Send")
	m = pickLabel(t, m, "off")
	rule := m.conf.device.Display.ReadRules[0]
	if rule.Delay == nil || *rule.Delay != 5 || rule.Send == nil || *rule.Send {
		t.Errorf("rule = %+v, want delay 5 and send off", rule)
	}
	if r := savedDevice(t, path).Display.ReadRules; len(r) != 1 || r[0].Delay == nil || *r[0].Delay != 5 {
		t.Errorf("saved %+v", r)
	}

	m = pickLabel(t, m, "Remove this one")
	if len(m.conf.device.Display.ReadRules) != 0 || m.choosing.settingGroup != recordPrefix+"display.read_rule" {
		t.Errorf("after removing: %+v, in %q", m.conf.device.Display.ReadRules, m.choosing.settingGroup)
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
	m = typeIn(t, m, "matrix:@dana:x")
	if ids := m.conf.base.Display.Identities[0].IDs; !slices.Equal(ids, []string{"matrix:@dana:x"}) {
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
