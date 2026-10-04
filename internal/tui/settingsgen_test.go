package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Every property of the config can be changed from settings: a row of its own in one
// listed group, or a hand-written row that covers it — none is left out, none listed
// twice.
func TestEveryPropertyHasARow(t *testing.T) {
	t.Parallel()
	groups := map[string]bool{}
	for _, g := range settingGroups {
		groups[g.key] = true
	}
	rows := map[string]int{}
	covered := map[string]bool{}
	for i := range settingsList {
		s := &settingsList[i]
		rows[s.key]++
		if !groups[s.group] {
			t.Errorf("%s is in group %q, which settings does not list", s.key, s.group)
		}
		for _, k := range s.covers {
			covered[k] = true
		}
	}
	for _, p := range config.Properties() {
		switch {
		case rows[p.Path] > 1:
			t.Errorf("%s has %d rows", p.Path, rows[p.Path])
		case rows[p.Path] == 0 && !covered[p.Path]:
			t.Errorf("%s has no row, and no hand-written row covers it", p.Path)
		}
	}
}

// An on/off property left unset follows its default: switched once it is written,
// switched back it is unset again rather than written as the default.
func TestAnOptionalSwitchComesBackToUnset(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn("look", "display.mouse")
	if got := rowFor(t, m, "Mouse").detail; got != "on" {
		t.Fatalf("mouse reads %q unset, want its default, on", got)
	}
	m = pickLabel(t, m, "Mouse")
	if v := m.conf.base.Display.Mouse; v == nil || *v {
		t.Fatalf("after one switch mouse = %v, want false written", v)
	}
	m = pickLabel(t, m, "Mouse")
	if v := m.conf.base.Display.Mouse; v != nil {
		t.Errorf("switched back mouse = %v, want it unset (the default)", *v)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Display.Mouse != nil {
		t.Error("the file kept a value equal to the default")
	}
}

// A generated number steps and is typed in place; a decimal and a text are typed in
// place; each is applied and saved.
func TestGeneratedRowsSetTheirProperty(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn("look", "display.fps")
	for range 3 {
		m, _ = press(t, m, keyText("+"))
	}
	if m.conf.base.Display.FPS != 3 {
		t.Errorf("fps after three + = %d", m.conf.base.Display.FPS)
	}
	m = m.settingsIn("spam", "spam.ratio")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "0.4")
	if m.conf.base.Spam.Ratio != 0.4 {
		t.Errorf("spam ratio = %v, want 0.4", m.conf.base.Spam.Ratio)
	}
	m = m.settingsIn("look", "display.theme.accent")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "#ff8800")
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Display.FPS != 3 || reloaded.Spam.Ratio != 0.4 || reloaded.Display.Theme.Accent != "#ff8800" {
		t.Errorf("saved fps %d ratio %v accent %q", reloaded.Display.FPS, reloaded.Spam.Ratio, reloaded.Display.Theme.Accent)
	}
	if item, _ := m.picker.selected(); m.picker.kind != pickerSetting || item.value != "display.theme.accent" {
		t.Errorf("after saving: picker %v on %q, want the group on the row", m.picker.kind, item.value)
	}
}

// A list is edited an entry at a time, starting from its default when unset: an entry
// moves (K/J), is added, changed, and removed by emptying it; each change is saved,
// and esc goes back to the group on the list's row.
func TestAListIsEditedAnEntryAtATime(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn("rooms", "display.rooms.sort")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picker.kind != pickerSettingEntries {
		t.Fatalf("enter on a list opened %v", m.picker.kind)
	}
	entries := func(m Model) []string { return m.settingEntries("display.rooms.sort") }
	if got := entries(m); !slices.Equal(got, []string{"unread", "recent", "name"}) {
		t.Fatalf("an unset list starts from %v, want its default", got)
	}
	m, _ = press(t, m, keyText("J")) // unread down a place
	if got := m.conf.base.Display.Rooms.Sort; !slices.Equal(got, []string{"recent", "unread", "name"}) {
		t.Errorf("after J: %v", got)
	}
	m = m.settingEntriesOpen("display.rooms.sort", settingAdd)
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "mentions")
	m = m.settingEntriesOpen("display.rooms.sort", "2") // name
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.editingSettingRow() || !m.prompt.fresh || m.prompt.input != "name" {
		t.Fatalf("enter on an entry: editing %v fresh %v input %q", m.editingSettingRow(), m.prompt.fresh, m.prompt.input)
	}
	m = typeIn(t, m, "")
	if got := m.conf.base.Display.Rooms.Sort; !slices.Equal(got, []string{"recent", "unread", "mentions"}) {
		t.Errorf("after adding mentions and emptying name: %v", got)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reloaded.Display.Rooms.Sort, []string{"recent", "unread", "mentions"}) {
		t.Errorf("saved %v", reloaded.Display.Rooms.Sort)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.kind != pickerSetting || m.choosing.setting != "display.rooms.sort" {
		t.Errorf("esc from the list: picker %v on %q, want the group on the row", m.picker.kind, m.choosing.setting)
	}
}

// What default.toml says of the setting under the cursor is shown beneath the list.
func TestTheSettingsTextIsShownBeneathTheList(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{Enabled: true})
	m = update(t, m, tea.WindowSizeMsg{Width: 130, Height: 40})
	m = m.settingsIn("look", "display.fps")
	if view := stripStyles(m.View().Content); !strings.Contains(view, "repainted") {
		t.Errorf("the fps row's text is not shown:\n%s", view)
	}
}
