package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The remove key (x, picker.remove) takes the row under the cursor out of any list of
// removable things: list setting entries, tag rules and rooms, card fields, settings records,
// tags, notification rules, scheduled messages, filing-picker places. A tag asks first.

// actionRow prefixes the value of every row that makes something (tagNew, settingAdd, …)
// rather than being a list item.
const actionRow = "\x00"

// removeUnderCursor removes the picker row under the cursor; handled is false where rows are
// not removable (or the row is an action row).
func (m Model) removeUnderCursor() (Model, tea.Cmd, bool) {
	item, ok := m.picker.selected()
	if !ok || strings.HasPrefix(item.value, actionRow) {
		return m, nil, false
	}
	switch m.picker.kind {
	case pickerSettingEntries:
		return answered(m.removeSettingEntry())
	case pickerTagEntries:
		return answered(m.removeTagEntry())
	case pickerPerson:
		m = m.closePicker()
		m.aimedAt.person.key, m.aimedAt.person.list = item.value, false
		return answered(m.submitPersonValue(""))
	case pickerSetting:
		return m.removeRecordRow(item.value)
	case pickerRuleList:
		i, err := strconv.Atoi(item.value)
		if err != nil || i < 0 || i >= len(m.aimedAt.ruleScopes) {
			return m, nil, false
		}
		m.aimedAt.rule = m.aimedAt.ruleScopes[i]
		m = m.closePicker()
		next, cmd := m.removeRule()
		reopened, more := next.openRuleList()
		return reopened, tea.Batch(cmd, more), true
	case pickerScheduled:
		return answered(m.acceptPick())
	case pickerRoomSpaces:
		checked := make(map[string]bool, len(m.picker.checked))
		for k, v := range m.picker.checked {
			checked[k] = v && k != item.value
		}
		m.picker.checked = checked
		return m, nil, true
	default:
		return m, nil, false
	}
}

// removeRecordRow removes the settings record a row is (rec:<table>#<i>).
func (m Model) removeRecordRow(key string) (Model, tea.Cmd, bool) {
	if page, ok := strings.CutPrefix(key, placePagePrefix); ok && m.choosing.settingGroup == "places" {
		name, isTag := domain.TagOf(page)
		if !isTag {
			return m, nil, false // a space is left from the rail, not deleted here
		}
		m = m.closePicker()
		m.confirm = confirmState{action: pendingDeleteTag, group: name}
		return m, nil, true
	}
	rest, ok := strings.CutPrefix(key, recordPrefix)
	if !ok {
		return m, nil, false
	}
	table, index, ok := strings.Cut(rest, "#")
	i, err := strconv.Atoi(index)
	if !ok || err != nil {
		return m, nil, false
	}
	t, ok := recordTable(recordPrefix + table)
	if !ok {
		return m, nil, false
	}
	return answered(m.removeRecord(t, i))
}
