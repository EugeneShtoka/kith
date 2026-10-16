package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// pressX presses the remove key and delivers what it starts.
func pressX(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := press(t, m, keyText("x"))
	return deliver(t, next, cmd)
}

// x removes a card's field from the file; on Add a field it does nothing.
func TestXRemovesACardsField(t *testing.T) {
	t.Parallel()
	m, dir := carding(t, nil)
	m = cardOf(t, m, "eli")
	m.picker.cursor = 1 // pets
	m = pressX(t, m)
	raw, _ := os.ReadFile(filepath.Join(dir, "Eli Stone.md"))
	if strings.Contains(string(raw), "pets") || !strings.Contains(string(raw), "origin") {
		t.Errorf("after x on pets:\n%s", raw)
	}
	m.picker.cursor = len(m.picker.items) - 1 // Add a field
	before := len(m.picker.items)
	m = m.clearStatus()
	if m = pressX(t, m); m.picker.kind != pickerPerson || len(m.picker.items) != before || m.status() != "" {
		t.Errorf("x on Add a field: picker %v, %d rows, said %q", m.picker.kind, len(m.picker.items), m.status())
	}
}

// x removes a settings record (a person's alias here), and asks before deleting a tag.
func TestXRemovesARecordAndAsksForATag(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{})
	cfg := m.conf.base.Clone()
	cfg.Display.Identities = []config.Identity{{Alias: "Dana", IDs: []string{"@dana:x"}}, {Alias: "Eli", IDs: []string{"@eli:x"}}}
	m, _ = m.applyConfig(cfg, "")
	m = m.settingsIn(recordPrefix+"display.identity", recordPrefix+"display.identity#0")
	m = pressX(t, m)
	if ids := m.conf.base.Display.Identities; len(ids) != 1 || ids[0].Alias != "Eli" {
		t.Errorf("identities after x on Dana = %+v", ids)
	}

	m = m.tagsOpen()
	m.picker.cursor = slices.IndexFunc(m.picker.items, func(i pickerItem) bool { return isTagGroup(strings.TrimPrefix(i.value, placePagePrefix)) })
	name, _ := domain.TagOf(strings.TrimPrefix(m.picker.items[m.picker.cursor].value, placePagePrefix))
	m = pressX(t, m)
	if m.confirm.action != pendingDeleteTag || m.confirm.group != name || m.configTag(name) < 0 {
		t.Errorf("x on tag %q: confirm %+v, tag still there %v", name, m.confirm, m.configTag(name) >= 0)
	}
}

// In the filing picker x unticks the place under the cursor, and the set applies as
// enter applies it; on New tag it does nothing.
func TestXUnticksAPlaceInTheFilingPicker(t *testing.T) {
	t.Parallel()
	m, f := filing(t)
	m, _ = press(t, m, keyText("S"))
	if !m.picker.ticked("!w:x") {
		t.Fatal("Work does not hold Alpha to begin with")
	}
	m = pressX(t, m)
	if m.picker.ticked("!w:x") {
		t.Error("x left Work ticked")
	}
	m.picker.cursor = len(m.picker.items) - 1 // New tag
	m = pressX(t, m)
	if m.picker.kind != pickerRoomSpaces || m.prompt.active() {
		t.Errorf("x on New tag: picker %v, prompt %v", m.picker.kind, m.prompt.kind)
	}
	next, cmd := m.acceptPick()
	_ = deliver(t, next, cmd)
	if !f.removed || f.removedFrom != "!w:x" {
		t.Errorf("removed from %q, want Work", f.removedFrom)
	}
}

// x removes the notification rule under the cursor and leaves the list open on the rest.
func TestXRemovesANotificationRule(t *testing.T) {
	t.Parallel()
	m, _ := ruling(t, config.Notifications{Rules: []config.Rule{
		{Match: "room:Standup", Sound: "a.ogg"},
		{Match: "room:Random", Sound: "b.ogg"},
	}})
	m, _ = m.openRuleList()
	m.picker.cursor = 0
	m = pressX(t, m)
	rules := m.conf.base.Notifications.Rules
	if len(rules) != 1 || rules[0].Match != "room:Random" {
		t.Errorf("rules after x = %+v", rules)
	}
	if m.picker.kind != pickerRuleList || len(m.picker.items) != 1 {
		t.Errorf("after x: picker %v with %d rows, want the list on what is left", m.picker.kind, len(m.picker.items))
	}
}
