package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Editing the layout from the app: renaming rooms, threads and rail groups, and
// rearranging the rail, by pointing at the thing (room IDs and group keys are not shown).

// renameRoom opens the prompt for the selected room's displayed name, prefilled.
func (m Model) renameRoom() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	m.aimedAt.renamingRoom = room.ID
	m = m.openPromptWith(promptRoomName, m.roomLabel(room))
	return m, nil
}

// submitRoomName writes the room alias; an empty name clears the override.
func (m Model) submitRoomName(name string) (Model, tea.Cmd) {
	roomID := m.aimedAt.renamingRoom
	m.aimedAt.renamingRoom = ""
	if roomID == "" {
		return m, nil
	}
	room, ok := m.roomByID(roomID)
	if !ok {
		return m, nil
	}
	display := m.prefs.display
	display.Names = config.SetName(display.Names, string(roomID), strings.TrimSpace(name))
	if strings.TrimSpace(name) == "" {
		return m.applyDisplay(display, "cleared the name override — showing "+isolate(room.DisplayName()))
	}
	return m.applyDisplay(display, "renamed to "+isolate(strings.TrimSpace(name)))
}

// renameThread opens the prompt for a thread's name, prefilled with the current
// (often guessed) name.
func (m Model) renameThread(root domain.EventID) (Model, tea.Cmd) {
	if root == "" {
		return m, nil
	}
	m.aimedAt.renamingThread = root
	m = m.openPromptWith(promptThreadName, m.threadName(root))
	return m, nil
}

// submitThreadName writes the thread alias; an empty name clears the override.
func (m Model) submitThreadName(name string) (Model, tea.Cmd) {
	root := m.aimedAt.renamingThread
	m.aimedAt.renamingThread = ""
	if root == "" {
		return m, nil
	}
	display := m.prefs.display
	display.Names = config.SetName(display.Names, config.NameTargetThread+string(root), strings.TrimSpace(name))
	if strings.TrimSpace(name) == "" {
		return m.applyDisplay(display, "cleared the thread's name")
	}
	return m.applyDisplay(display, "named this thread "+isolate(strings.TrimSpace(name)))
}

// renameGroup opens the prompt for the selected rail group's label, prefilled.
func (m Model) renameGroup() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if isTagGroup(entry.key) {
		// A tag's name is what everything refers to it by: change it in its [[tag]].
		m = m.say("a tag is named in its [[tag]] block: " + isolate(entry.label))
		return m, nil
	}
	m.aimedAt.renamingGroup = entry.key
	m = m.openPromptWith(promptGroupName, entry.label)
	return m, nil
}

// submitGroupName writes the rail rename; an empty name clears the override.
func (m Model) submitGroupName(label string) (Model, tea.Cmd) {
	key := m.aimedAt.renamingGroup
	m.aimedAt.renamingGroup = ""
	if key == "" {
		return m, nil
	}
	display := m.prefs.display
	label = strings.TrimSpace(label)
	display.Names = config.SetName(display.Names, config.GroupTarget(key), label)
	if label == "" {
		return m.applyDisplay(display, "cleared the label override for "+key)
	}
	return m.applyDisplay(display, "renamed to "+isolate(label))
}

// moveGroup shifts the selected rail group by delta and writes the new order. The first
// move writes the whole visible arrangement, so newly joined spaces then append at the
// end.
func (m Model) moveGroup(delta int) (Model, tea.Cmd) {
	from := m.rail.cursor
	to := from + delta
	if from < 0 || from >= len(m.rail.groups) || to < 0 || to >= len(m.rail.groups) {
		return m, nil
	}
	order := railOrder(m.rail.groups)
	// The order carries separator tokens, so indices differ from the rail's.
	fromToken, toToken := slices.Index(order, m.rail.groups[from].key), slices.Index(order, m.rail.groups[to].key)
	if fromToken < 0 || toToken < 0 {
		return m, nil
	}
	order = moveToken(order, fromToken, toToken)

	moved := m.rail.groups[from]
	display := m.prefs.display
	display.Rail.Order = order
	// The cursor follows the group, or a second press would appear to undo the first.
	return m.applyRailFocused(display, moved.key, "moved "+isolate(moved.label))
}

// railOrder is the rail's current arrangement as an order list, separators included.
func railOrder(groups []group) []string {
	out := make([]string, 0, len(groups)+2)
	for _, g := range groups {
		out = append(out, g.key)
		if g.sepAfter {
			out = append(out, "-")
		}
	}
	return out
}

// moveToken moves one entry of an order list to another entry's position, carrying it
// over any separators in between rather than swapping with them.
func moveToken(order []string, from, to int) []string {
	if from == to {
		return order
	}
	token := order[from]
	rest := slices.Delete(slices.Clone(order), from, from+1)
	return slices.Insert(rest, min(to, len(rest)), token)
}

// hideGroup removes the selected group from the rail; showHiddenGroups undoes it.
func (m Model) hideGroup() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if len(m.rail.groups) == 1 {
		m = m.say("that's the only group left")
		return m, nil
	}
	display := m.prefs.display
	display.Rail.Hidden = appendUnique(display.Rail.Hidden, entry.key)
	if m.rail.cursor >= len(m.rail.groups)-1 {
		m.rail.cursor = max(len(m.rail.groups)-2, 0)
	}
	return m.applyDisplay(display, "hid "+entry.label+" — press "+m.showKeyHint()+" to bring it back")
}

// showHiddenGroups offers the hidden groups to bring back.
func (m Model) showHiddenGroups() (Model, tea.Cmd) {
	hidden := m.prefs.display.Rail.Hidden
	if len(hidden) == 0 {
		m = m.say("no hidden groups")
		return m, nil
	}
	items := make([]pickerItem, 0, len(hidden))
	for _, key := range hidden {
		items = append(items, pickerItem{
			label:  isolate(m.groupLabelFor(key)),
			detail: key,
			value:  key,
			match:  key,
		})
	}
	m.picker = newPicker(pickerHidden, items)
	return m, nil
}

// unhideGroup brings a hidden group back.
func (m Model) unhideGroup(key string) (Model, tea.Cmd) {
	m = m.closePicker()
	display := m.prefs.display
	display.Rail.Hidden = without(display.Rail.Hidden, key)
	return m.applyDisplay(display, "showing "+isolate(m.groupLabelFor(key))+" again")
}

// groupLabelFor is a group key's displayed label, also for hidden groups.
func (m Model) groupLabelFor(key string) string {
	if label := m.prefs.display.NameFor(config.GroupTarget(key)); label != "" {
		return label
	}
	if label, ok := builtInLabels[key]; ok {
		return label
	}
	return key // a space's key is its name
}

// toggleFirstNameOnly flips the selected space's first-name rule.
func (m Model) toggleFirstNameOnly() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if !isSpaceGroup(entry.key) && !isTagGroup(entry.key) {
		m = m.say("name rules apply to spaces and tags, not to " + entry.label)
		return m, nil
	}
	display := m.prefs.display
	rules, on := toggleSpaceRule(display.SpaceRules, entry.key)
	display.SpaceRules = rules
	state := "off"
	if on {
		state = "on"
	}
	return m.applyDisplay(display, "first names only in "+entry.label+": "+state)
}

// toggleSpaceRule flips first_name_only for one space, returning the new rules and
// whether it is now on. A rule left with nothing on is dropped.
func toggleSpaceRule(rules []config.SpaceRule, space string) ([]config.SpaceRule, bool) {
	out := make([]config.SpaceRule, 0, len(rules)+1)
	found, now := false, false
	for _, rule := range rules {
		if rule.Space != space {
			out = append(out, rule)
			continue
		}
		found = true
		now = !rule.FirstNameOnly
		if now {
			rule.FirstNameOnly = true
			out = append(out, rule)
		}
	}
	if !found {
		now = true
		out = append(out, config.SpaceRule{Space: space, FirstNameOnly: true})
	}
	return out, now
}

// currentGroup is the rail group under the cursor.
func (m Model) currentGroup() (group, bool) { return m.rail.at() }

// showKeyHint names the key that brings a hidden group back.
func (m Model) showKeyHint() string {
	if key := m.keys.keyHint(scopeRail, actShowHidden); key != "" {
		return key
	}
	return "the show-hidden key"
}

// appendUnique returns a copy of items with s added, unless it is already there.
func appendUnique(items []string, s string) []string {
	if slices.Contains(items, s) {
		return items
	}
	return append(slices.Clip(items), s)
}
