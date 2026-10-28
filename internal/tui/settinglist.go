package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// A list setting is edited an entry at a time, with an add row last. Enter edits an
// entry (emptied = removed), x removes, K/J move, esc goes back. Each change is
// applied and saved at once.

// settingAdd is the add row's value; no entry index is spelled so.
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
		label, detail := e, ""
		if s.entryLabel != nil {
			label, detail = s.entryLabel(e), e
		}
		items = append(items, pickerItem{label: label, detail: detail, value: strconv.Itoa(i), match: label + " " + e})
	}
	items = append(items, pickerItem{label: "Add an entry", value: settingAdd, match: "Add an entry"})
	m.choosing.setting, m.choosing.settingGroup = key, s.group
	spec := pickerSpecs[pickerSettingEntries]
	spec.title = "Settings: " + m.settingGroupLabel(s.group) + " · " + s.label
	m.picker = newPickerWith(pickerSettingEntries, spec, items).at(at)
	return m
}

// settingEntries is the entries in force: the config's, else default.toml's, so
// editing one entry of a default list starts from all of it.
func (m Model) settingEntries(key string) []string {
	if s, ok := m.setting(key); ok && s.entries != nil {
		return s.entries(m.settings())
	}
	return nil
}

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

// submitSettingEntry adds a new entry at the end, replaces an edited one, removes an emptied one.
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

func (m Model) removeSettingEntry() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok || item.value == settingAdd {
		return m, nil
	}
	m.choosing.settingEntry = atoiSafe(item.value)
	return m.submitSettingEntry("")
}

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

// writeSettingList applies and shows a list setting's new entries; a refusal says
// why and changes nothing.
func (m Model) writeSettingList(key string, entries []string, at string) (Model, tea.Cmd) {
	s, ok := m.setting(key)
	if !ok {
		return m, nil
	}
	cfg := m.settings().Clone()
	if s.setEntries == nil {
		return m, nil
	}
	if err := s.setEntries(&cfg, entries); err != nil {
		return m.settingEntriesOpen(key, at).sayErr(s.label, err), nil
	}
	next, cmd := m.applySettings(cfg, s.label+": "+s.show(cfg))
	return next.settingEntriesOpen(key, at), cmd
}

// settingSetOpen ticks a set setting's values among its choices.
func (m Model) settingSetOpen(s setting) Model {
	ticked := map[string]bool{}
	for _, v := range s.entries(m.settings()) {
		ticked[v] = true
	}
	items := make([]pickerItem, 0, len(s.choices))
	for _, c := range s.choices {
		items = append(items, pickerItem{label: c.label, detail: c.detail, value: c.value, match: c.label + " " + c.value})
	}
	m.choosing.setting = s.key
	spec := pickerSpecs[pickerSettingSet]
	spec.title = "Settings: " + m.settingGroupLabel(s.group) + " · " + s.label
	m.picker = newPickerWith(pickerSettingSet, spec, items)
	m.picker.checked = ticked
	return m
}

// writeSettingSet saves the ticked values, in the choices' order; a set refused stays
// open with the reason.
func (m Model) writeSettingSet(values []string) (Model, tea.Cmd) {
	s, ok := m.setting(m.choosing.setting)
	if !ok || s.setEntries == nil {
		return m.closePicker(), nil
	}
	cfg := m.settings().Clone()
	if err := s.setEntries(&cfg, values); err != nil {
		return m.sayErr(s.label, err), nil
	}
	next, cmd := m.applySettings(cfg, s.label+": "+s.show(cfg))
	return next.settingsIn(s.group, s.key), cmd
}
