package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Every binding of every key table has a row, under its table in Keys.
func TestEveryBindingHasARow(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{Enabled: true})
	for _, table := range config.KeyTables() {
		if _, ok := findSetting(keyTableKey(table.Name)); !ok {
			t.Errorf("key table %q has no row in Keys", table.Name)
		}
		rows := m.settingsIn(keyTableKey(table.Name), "").picker.items
		for _, b := range table.Bindings {
			found := false
			for _, item := range rows {
				found = found || item.value == keysPrefix+b.Path
			}
			if !found {
				t.Errorf("%s has no row in %s", b.Path, keyTableLabel(table.Name))
			}
		}
	}
}

// A binding is typed in place and works at once; one that clashes is refused with the
// reason and changes nothing; empty restores the default; "-" binds nothing.
func TestABindingIsChangedFromSettings(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn(keyTableKey("timeline"), keysPrefix+"timeline.reply")
	set := func(m Model, keys string) Model {
		m = m.settingsIn(keyTableKey("timeline"), keysPrefix+"timeline.reply")
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		return typeIn(t, m, keys)
	}
	m = set(m, "R")
	if act := m.keys.lookup("R", scopeTimeline); act != actReply {
		t.Errorf("R in the timeline = %v, want reply at once", act)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Keys.Binding("timeline.reply"); got != "R" {
		t.Errorf("saved reply = %q", got)
	}

	m = set(m, "e") // react's key
	if got, _ := m.conf.base.Keys.Binding("timeline.reply"); got != "R" {
		t.Errorf("a clashing key was written: reply = %q", got)
	}
	if !strings.Contains(m.status(), "timeline.reply") {
		t.Errorf("status = %q, want the clash explained", m.status())
	}

	m = set(m, "")
	if got, _ := m.conf.base.Keys.Binding("timeline.reply"); got != "r" {
		t.Errorf("empty left reply = %q, want its default r", got)
	}
	m = set(m, "-")
	if got := rowFor(t, m, "Reply to the selected message").detail; got != "none" {
		t.Errorf("unbound reply reads %q, want none", got)
	}
	if act := m.keys.lookup("r", scopeTimeline); act == actReply {
		t.Error("r still replies after - bound it to nothing")
	}
}

// The jump shortcuts are records in Keys: listed, and removed from there; esc walks
// back from a key table to Keys, then to the groups.
func TestJumpShortcutsAndKeysNavigation(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{Enabled: true})
	cfg := m.conf.base.Clone()
	cfg.Keys.Jump = config.Jumps{{Chord: "g w", Target: "space:Work"}}
	m, _ = m.applyConfig(cfg, "")
	m = m.settingsIn("keys", "")
	if got := rowFor(t, m, "Jump shortcuts").detail; got != "1" {
		t.Errorf("jump shortcuts read %q, want 1", got)
	}
	m = pickLabel(t, m, "Jump shortcuts")
	m = pickLabel(t, m, "g w")
	m = pickLabel(t, m, "Remove this one")
	if len(m.conf.base.Keys.Jump) != 0 {
		t.Errorf("jumps = %+v after removing", m.conf.base.Keys.Jump)
	}

	m = m.settingsIn(keyTableKey("timeline"), "")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.choosing.settingGroup != "keys" {
		t.Errorf("esc from a key table: in %q, want Keys", m.choosing.settingGroup)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.kind != pickerSettingGroups {
		t.Errorf("esc from Keys: picker %v, want the groups", m.picker.kind)
	}
}
