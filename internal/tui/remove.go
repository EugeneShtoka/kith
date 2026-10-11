package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The remove key (x, picker.remove) takes the row under the cursor out of whatever list
// it is in, wherever a list holds things that can go: a list setting's entry, a tag's
// rule or room, a phonebook card's field, a record of a settings table (a person's
// alias, a name you gave, a read or media rule), a tag, a notification rule, a
// scheduled message, and a place in the filing picker (unticked, applied with the
// rest). What goes for good asks first where its own gesture does (a tag).

// actionRow begins the value of every row that makes something rather than being a
// thing in the list (tagNew, settingAdd, personAdd, and the like).
const actionRow = "\x00"

// removeUnderCursor removes the open picker's row under the cursor; handled is false
// for a list whose rows are not things to remove, or a row that is not one (an Add row).
func (m Model) removeUnderCursor() (Model, tea.Cmd, bool) {
	item, ok := m.picker.selected()
	// A row that makes something (Add a field, New tag, New space) is valued with a
	// leading NUL, which no thing in a list is: it is not removed, in any list.
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
	case pickerTags:
		m = m.closePicker()
		m.confirm = confirmState{action: pendingDeleteTag, group: item.value}
		return m, nil, true
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

// removeRecordRow removes the settings table's record a row is (rec:<table>#<i>); a row
// of anything else is not removed.
func (m Model) removeRecordRow(key string) (Model, tea.Cmd, bool) {
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
