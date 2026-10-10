package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// A list setting (the rail's order, priority, a list of places) is edited an entry at
// a time: its entries, then a row to add one. Enter types an entry on its row (an
// emptied one is removed), x removes it, K and J move it, and esc goes back to the
// group. Each change
// is applied and saved at once, as every setting is.

// settingAdd is the entries list's "Add an entry" row; no entry index is spelled so.
const settingAdd = "\x00add"

// settingEntriesOpen shows a list setting's entries, the cursor on the row valued at.
func (m Model) settingEntriesOpen(key, at string) Model {
	s, ok := m.setting(key)
	if !ok {
		return m.settingsTop(m.choosing.settingGroup)
	}
	entries := m.settingEntries(key)
	items := make([]pickerItem, 0, len(entries)+1)
	for i, e := range entries {
		items = append(items, pickerItem{label: e, value: strconv.Itoa(i), match: e})
	}
	items = append(items, pickerItem{label: "Add an entry", value: settingAdd, match: "Add an entry"})
	m.choosing.setting, m.choosing.settingGroup = key, s.group
	spec := pickerSpecs[pickerSettingEntries]
	spec.title = "Settings: " + m.settingGroupLabel(s.group) + " · " + s.label
	m.picker = newPickerWith(pickerSettingEntries, spec, items).at(at)
	return m
}

// settingEntries is a list setting's entries in force: what the config sets, else
// default.toml's, so changing one entry of a default list starts from all of it.
func (m Model) settingEntries(key string) []string {
	if s, ok := m.setting(key); ok && s.entries != nil {
		return s.entries(m.conf.base)
	}
	return nil
}

// chooseSettingEntry types the entry under the cursor on its row, or a new one on the
// add row.
func (m Model) chooseSettingEntry(value string) (Model, tea.Cmd) {
	if value == settingAdd {
		m.choosing.settingEntry = -1
		return m.openPrompt(promptSettingEntry), nil
	}
	entries := m.settingEntries(m.choosing.setting)
	i := atoiSafe(value)
	if i < 0 || i >= len(entries) {
		return m.settingEntriesOpen(m.choosing.setting, ""), nil
	}
	m.choosing.settingEntry = i
	m = m.openPromptWith(promptSettingEntry, entries[i])
	m.prompt.fresh = true
	return m, nil
}

// submitSettingEntry writes the typed entry: a new one is added at the end, an edited
// one replaced, an emptied one removed.
func (m Model) submitSettingEntry(input string) (Model, tea.Cmd) {
	key, i := m.choosing.setting, m.choosing.settingEntry
	entries := m.settingEntries(key)
	input = strings.TrimSpace(input)
	var at string
	switch {
	case i < 0 && input == "":
		return m.settingEntriesOpen(key, settingAdd), nil
	case i < 0:
		entries, at = append(entries, input), strconv.Itoa(len(entries))
	case i >= len(entries):
		return m.settingEntriesOpen(key, ""), nil
	case input == "":
		entries = append(entries[:i:i], entries[i+1:]...)
		at = strconv.Itoa(min(i, len(entries)))
		if len(entries) == 0 {
			at = settingAdd
		}
	default:
		entries[i], at = input, strconv.Itoa(i)
	}
	return m.writeSettingList(key, entries, at)
}

// removeSettingEntry takes the entry under the cursor out of its list, as emptying it
// would.
func (m Model) removeSettingEntry() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok || item.value == settingAdd {
		return m, nil
	}
	m.choosing.settingEntry = atoiSafe(item.value)
	return m.submitSettingEntry("")
}

// moveSettingEntry moves the entry under the cursor by delta, for an ordered list.
func (m Model) moveSettingEntry(delta int) (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok || item.value == settingAdd {
		return m, nil
	}
	key := m.choosing.setting
	entries := m.settingEntries(key)
	i := atoiSafe(item.value)
	j := i + delta
	if i < 0 || i >= len(entries) || j < 0 || j >= len(entries) {
		return m, nil
	}
	entries[i], entries[j] = entries[j], entries[i]
	return m.writeSettingList(key, entries, strconv.Itoa(j))
}

// writeSettingList applies a list setting's new entries and shows them again, the
// cursor on the row valued at; a config that refuses them says why and changes
// nothing.
func (m Model) writeSettingList(key string, entries []string, at string) (Model, tea.Cmd) {
	s, ok := m.setting(key)
	if !ok {
		return m, nil
	}
	cfg := m.conf.base.Clone()
	if s.setEntries == nil {
		return m, nil
	}
	if err := s.setEntries(&cfg, entries); err != nil {
		return m.settingEntriesOpen(key, at).sayErr(s.label, err), nil
	}
	next, cmd := m.applyConfig(cfg, s.label+": "+s.show(cfg))
	return next.settingEntriesOpen(key, at), cmd
}
