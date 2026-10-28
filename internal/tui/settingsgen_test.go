package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Every property has exactly one row: its own in a listed group, or a hand-written
// row that covers it.
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

// An unset on/off property switched back to its default is unset, not written.
func TestAnOptionalSwitchComesBackToUnset(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
	m = m.settingsIn("look", "display.mouse")
	if got := rowFor(t, m, "Mouse").detail; got != "on" {
		t.Fatalf("mouse reads %q unset, want its default, on", got)
	}
	m = pickLabel(t, m, "Mouse")
	if v := m.conf.device.Display.Mouse; v == nil || *v {
		t.Fatalf("after one switch mouse = %v, want false written", v)
	}
	m = pickLabel(t, m, "Mouse")
	if v := m.conf.device.Display.Mouse; v != nil {
		t.Errorf("switched back mouse = %v, want it unset (the default)", *v)
	}
	if savedDevice(t, path).Display.Mouse != nil {
		t.Error("the file kept a value equal to the default")
	}
}

func TestGeneratedRowsSetTheirProperty(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
	m = m.settingsIn("look", "display.fps")
	for range 3 {
		m, _ = press(t, m, keyText("+"))
	}
	if m.conf.device.Display.FPS != 3 {
		t.Errorf("fps after three + = %d", m.conf.device.Display.FPS)
	}
	m = m.settingsIn("media", "display.media.audio.speed")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "1.5")
	if m.conf.device.Display.Media.Audio.Speed != 1.5 {
		t.Errorf("audio speed = %v, want 1.5", m.conf.device.Display.Media.Audio.Speed)
	}
	m = m.settingsIn("look", "display.theme.accent")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "#ff8800")
	dev := savedDevice(t, path)
	if dev.Display.FPS != 3 || dev.Display.Media.Audio.Speed != 1.5 || dev.Display.Theme.Accent != "#ff8800" {
		t.Errorf("saved fps %d speed %v accent %q", dev.Display.FPS, dev.Display.Media.Audio.Speed, dev.Display.Theme.Accent)
	}
	if item, _ := m.picker.selected(); m.picker.kind != pickerSetting || item.value != "display.theme.accent" {
		t.Errorf("after saving: picker %v on %q, want the group on the row", m.picker.kind, item.value)
	}
}

// A list starts from its default when unset; entries move (K/J), are added, changed
// and removed by emptying; each change is saved, and esc returns to the list's row.
func TestAListIsEditedAnEntryAtATime(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
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
	if got := m.conf.device.Display.Rooms.Sort; !slices.Equal(got, []string{"recent", "unread", "name"}) {
		t.Errorf("after J: %v", got)
	}
	m = m.settingEntriesOpen("display.rooms.sort", settingAdd)
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeIn(t, m, "mentions")
	m = m.settingEntriesOpen("display.rooms.sort", "2") // name
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.prompt.kind != promptSettingEntry || !m.prompt.fresh || m.prompt.input != "name" {
		t.Fatalf("enter on an entry: editing %v fresh %v input %q", (m.prompt.kind == promptSettingEntry), m.prompt.fresh, m.prompt.input)
	}
	m = typeIn(t, m, "")
	if got := m.conf.device.Display.Rooms.Sort; !slices.Equal(got, []string{"recent", "unread", "mentions"}) {
		t.Errorf("after adding mentions and emptying name: %v", got)
	}
	if reloaded := savedDevice(t, path); !slices.Equal(reloaded.Display.Rooms.Sort, []string{"recent", "unread", "mentions"}) {
		t.Errorf("saved %v", reloaded.Display.Rooms.Sort)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.kind != pickerSetting || m.choosing.setting != "display.rooms.sort" {
		t.Errorf("esc from the list: picker %v on %q, want the group on the row", m.picker.kind, m.choosing.setting)
	}
}

func TestTheSettingsTextIsShownBeneathTheList(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{})
	m = update(t, m, tea.WindowSizeMsg{Width: 130, Height: 40})
	m = m.settingsIn("look", "display.fps")
	if view := stripStyles(m.View().Content); !strings.Contains(view, "repainted") {
		t.Errorf("the fps row's text is not shown:\n%s", view)
	}
}

// x removes the entry under the cursor and saves; nothing on the add row, typed
// while filtering.
func TestXRemovesAListEntry(t *testing.T) {
	t.Parallel()
	m, path := opened(t, config.Notifications{})
	m = m.settingEntriesOpen("display.rooms.sort", "1") // recent
	m = pressedThrough(t, m, keyText("x"))
	if got := m.conf.device.Display.Rooms.Sort; !slices.Equal(got, []string{"unread", "name"}) {
		t.Fatalf("after x on recent: %v", got)
	}
	if item, _ := m.picker.selected(); m.picker.kind != pickerSettingEntries || item.label != "name" {
		t.Errorf("after x: picker %v on %q, want the list on the next entry", m.picker.kind, item.label)
	}
	if reloaded := savedDevice(t, path); !slices.Equal(reloaded.Display.Rooms.Sort, []string{"unread", "name"}) {
		t.Errorf("saved %v", reloaded.Display.Rooms.Sort)
	}
	m = m.settingEntriesOpen("display.rooms.sort", settingAdd)
	m, _ = press(t, m, keyText("x"))
	if got := m.conf.device.Display.Rooms.Sort; len(got) != 2 {
		t.Errorf("x on the add row: %v", got)
	}
	m = m.settingEntriesOpen("display.rooms.sort", "0")
	m, _ = press(t, m, keyText("i"))
	m, _ = press(t, m, keyText("x"))
	if got := m.conf.device.Display.Rooms.Sort; len(got) != 2 || m.picker.filter != "x" {
		t.Errorf("x while filtering: list %v, filter %q", got, m.picker.filter)
	}
}
