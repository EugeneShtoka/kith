package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A picker is a modal chooser over a list of things. A picker with letter actions
// (e.g. `a` to alias a person) is modal: it starts in navigate mode, `i` enters
// filter mode and esc steps back out. A picker whose only action is "choose this
// one" filters from the first keystroke.

// pickerWalk is what a chooser walk in progress is for, beyond its items.
type pickerWalk struct {
	// identity is the identity edit being walked through (picker, then prompt).
	identity pendingIdentity
	// setting is the preference being changed while its value is chosen.
	setting string
	// speedScopes is where a voice-note speed may be remembered, by picker row.
	speedScopes []ruleTarget
	// tag is where the tag editor is (tageditor.go).
	tag tagEditing
}

// pickerMode is whether keystrokes move the cursor or narrow the list.
type pickerMode int

const (
	pickerNavigate pickerMode = iota
	pickerFilter
)

// pickerKind identifies a picker; pickerSpecs gives its title and layout.
type pickerKind int

const (
	pickerNone pickerKind = iota
	pickerEmoji
	pickerReaction // emoji grid whose choice becomes a reaction
	pickerPeople
	pickerIdentity
	pickerColor
	pickerContext // which link/code in a message to act on; see context.go
	pickerHidden
	pickerRuleScope
	pickerSpeedScope
	pickerRuleList
	pickerRulePreset
	pickerSetting
	pickerSettingValue
	pickerDNDScope
	pickerMuteScope
	pickerDNDFor
	pickerScheduled // pending scheduled messages; choosing one cancels it
	pickerThread
	pickerRoomSpaces
	pickerDictionaries
	pickerFrequencies
	pickerCompletionModel
	pickerNewRoom
	pickerJump // the switcher; see jump.go
	pickerTags
	pickerTagEdit
	pickerTagEntries
)

// pickerSpec is a kind's fixed properties, copied into the picker when it opens.
type pickerSpec struct {
	title string
	// modal marks a picker with letter actions, which starts in navigate mode.
	modal bool
	// grid renders items as a grid of values (emoji) rather than labeled rows.
	grid bool
	// multi makes a checkbox set: space ticks, enter applies the whole set. It
	// implies modal, since space is a character in filter mode.
	multi bool
}

var pickerSpecs = map[pickerKind]pickerSpec{
	pickerEmoji:           {title: "Emoji", grid: true},
	pickerReaction:        {title: "React with", grid: true},
	pickerPeople:          {title: "People in this room", modal: true},
	pickerIdentity:        {title: "Add to which person?"},
	pickerColor:           {title: "Color"},
	pickerContext:         {}, // title supplied at the call site
	pickerScheduled:       {title: "Scheduled — choose one to cancel"},
	pickerHidden:          {title: "Show which group again?"},
	pickerRuleScope:       {title: "Notifications for what?"},
	pickerSpeedScope:      {title: "Play at this speed for what?"},
	pickerRuleList:        {title: "Notification rules"},
	pickerRulePreset:      {title: "Notify how?"},
	pickerSetting:         {title: "Settings"},
	pickerSettingValue:    {title: "Set it to"},
	pickerDNDScope:        {title: "Do not disturb for what?"},
	pickerMuteScope:       {title: "Mute sound for what?"},
	pickerDNDFor:          {title: "For how long?"},
	pickerThread:          {title: "Threads in this room"},
	pickerRoomSpaces:      {title: "Which spaces and tags hold this room?", modal: true, multi: true},
	pickerDictionaries:    {title: "Install spelling dictionaries for what you write?", modal: true, multi: true},
	pickerFrequencies:     {title: "Install word counts, so typos that are also words get caught?", modal: true, multi: true},
	pickerCompletionModel: {title: "Install the completion model, to run on this machine?", modal: true, multi: true},
	pickerNewRoom:         {title: "Create what?"},
	pickerJump:            {title: "Go to"},
	pickerTags:            {title: "Tags"},
	pickerTagEdit:         {}, // titled with the tag
	pickerTagEntries:      {}, // titled with the tag and the list
}

// pickerItem is one row: label shown, detail dimmed, value acted on, and match the
// text filtering narrows against (defaults to label+detail).
type pickerItem struct {
	label  string
	detail string
	value  string
	match  string
	// room is where the row's thing lives, for lists spanning rooms; empty means the
	// open room.
	room domain.RoomID
}

// picker is the open chooser.
type picker struct {
	kind pickerKind
	// then is the action of the key that opened a pickerContext, applied on accept.
	then   action
	spec   pickerSpec
	mode   pickerMode
	filter string
	// all is every item; items is the filtered view the cursor indexes into.
	all    []pickerItem
	items  []pickerItem
	cursor int
	// checked is the ticked rows of a multi picker, keyed by value so filtering
	// cannot move ticks.
	checked map[string]bool
}

func (p picker) active() bool { return p.kind != pickerNone }

func (p picker) ticked(value string) bool { return p.checked[value] }

// tick flips the row under the cursor of a multi picker.
func (p picker) tick() picker {
	item, ok := p.selected()
	if !ok || !p.spec.multi {
		return p
	}
	p.checked = withEntry(p.checked, item.value, !p.checked[item.value])
	return p
}

// checkedValues is every ticked value, in list order.
func (p picker) checkedValues() []string {
	out := make([]string, 0, len(p.checked))
	for _, item := range p.all {
		if p.checked[item.value] {
			out = append(out, item.value)
		}
	}
	return out
}

func (p picker) selected() (pickerItem, bool) {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return pickerItem{}, false
	}
	return p.items[p.cursor], true
}

func newPicker(kind pickerKind, items []pickerItem) picker {
	return newPickerWith(kind, pickerSpecs[kind], items)
}

// newPickerWith is newPicker with the spec supplied by the caller.
func newPickerWith(kind pickerKind, spec pickerSpec, items []pickerItem) picker {
	p := picker{kind: kind, spec: spec, all: items}
	if !spec.modal {
		p.mode = pickerFilter
	}
	return p.refilter()
}

// newCheckedPicker opens a multi picker with some rows already ticked.
func newCheckedPicker(kind pickerKind, items []pickerItem, checked map[string]bool) picker {
	p := newPicker(kind, items)
	p.checked = checked
	return p
}

// refilter recomputes the visible items: prefix matches first, then substring
// matches, the same order as the mention and emoji completions.
func (p picker) refilter() picker {
	query := strings.ToLower(strings.TrimSpace(p.filter))
	if query == "" {
		p.items = p.all
	} else {
		var prefix, contains []pickerItem
		for _, item := range p.all {
			text := strings.ToLower(item.matchText())
			switch {
			case strings.HasPrefix(text, query):
				prefix = append(prefix, item)
			case strings.Contains(text, query):
				contains = append(contains, item)
			}
		}
		p.items = slices.Concat(prefix, contains)
	}
	p.cursor = clampIndex(p.cursor, len(p.items))
	return p
}

// matchText is what filtering matches; label isolate marks are not typed, so stripped.
func (i pickerItem) matchText() string {
	if i.match != "" {
		return stripIsolates(i.match)
	}
	return stripIsolates(i.label + " " + i.detail)
}

// handlePickerKey drives the open picker; handled is false for keys it does not claim.
func (m Model) handlePickerKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	open := m.picker.active()
	mdl, cmd, handled := m.pickerKey(key)
	// Closing replaces a full pane at once; repaint so mis-measured glyph cells from
	// the picker cannot survive in the timeline.
	if open && !mdl.picker.active() {
		return mdl, tea.Batch(cmd, repaint()), handled
	}
	return mdl, cmd, handled
}

func (m Model) pickerKey(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	if m.picker.mode == pickerFilter {
		if text := key.Text; text != "" {
			return m.store(fieldFilter, m.editorFor(fieldFilter).insert(text)), nil, true
		}
		// Only the editing keys that keep the caret at the end: arrows walk the list.
		if ed, ok := m.editorFor(fieldFilter).editTail(key, m.keys); ok {
			return m.store(fieldFilter, ed), nil, true
		}
	}
	switch m.keys.lookup(key.String(), scopePicker) {
	case actFilter:
		m.picker.mode = pickerFilter
		return m, nil, true
	case actRenameThread:
		// Handled here, not in pickerAction: the thread list is always filtering, and
		// this binding is a chord, which produces no text.
		if m.picker.kind == pickerThread {
			return answered(m.renameSelectedThread())
		}
	case actTogglePick:
		m.picker = m.picker.tick()
		return m, nil, true
	case actAcceptPick:
		return answered(m.acceptPick())
	case actClosePick:
		// esc first leaves filter mode (modal) or clears the query (non-modal), and
		// only then closes.
		if m.picker.spec.modal && m.picker.mode == pickerFilter {
			m.picker.mode = pickerNavigate
		} else if m.picker.filter == "" {
			return m.closePicker(), nil, true
		}
		m.picker.filter = ""
		m.picker = m.picker.refilter()
		return m, nil, true
	}
	if mdl, cmd, acted := m.pickerAction(key); acted {
		return mdl, cmd, true
	}
	if next, moved := m.movePicker(key); moved {
		return next, nil, true
	}
	return m, nil, false
}

// movePicker applies a motion binding to the picker's cursor.
func (m Model) movePicker(key tea.KeyPressMsg) (Model, bool) {
	perRow := 1
	if m.picker.spec.grid {
		perRow = m.pickerColumns()
	}
	next, handled := m.picker.move(m.keys.lookup(key.String(), scopeNav), perRow, m.take(), m.pickerBody())
	if handled {
		m.picker = next
	}
	return m, handled
}

// move is the cursor arithmetic for one motion. Grid pickers step vertically by a
// row's width, and a page is the page rows on screen (navDelta, as every pane). A
// count multiplies steps, not pages; with the ends it names a row (vim's `12G`).
func (p picker) move(act action, perRow, count, page int) (picker, bool) {
	n := len(p.items)
	delta := 0
	switch act {
	case actDown:
		delta = perRow * count
	case actUp:
		delta = -perRow * count
	case actOpen:
		delta = count // "forward" in a grid, where down means a whole row
	case actBack:
		delta = -count
	case actPageDown, actPageUp, actHalfPageDown, actHalfPageUp:
		rows, _ := navDelta(act, count, page, 0)
		delta = perRow * rows
	case actSelectNewest, actScrollNewest, actSelectOldest, actScrollOldest:
		if count > 1 {
			p.cursor = clampIndex(count-1, n)
			return p, true
		}
		delta = n
		if act == actSelectOldest || act == actScrollOldest {
			delta = -n
		}
	default:
		return p, false
	}
	p.cursor = moveCursor(p.cursor, n, delta, false)
	return p, true
}

// pickerAction applies the people picker's letter actions in navigate mode.
func (m Model) pickerAction(key tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	if m.picker.mode != pickerNavigate || m.picker.kind != pickerPeople {
		return m, nil, false
	}
	switch m.keys.lookup(key.String(), scopePicker) {
	case actKick:
		return answered(m.askKickSelected())
	case actBan:
		return answered(m.askBanSelected())
	case actName:
		return answered(m.aliasSelectedPerson())
	default:
		return m, nil, false
	}
}

// atoiSafe reads an index a picker item carried, or -1.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func (m Model) closePicker() Model {
	m.picker = picker{}
	return m
}

// repaint clears and redraws the screen. Spent where a full pane is replaced at once:
// when the program and terminal disagree on a glyph's width, cells from the previous
// frame otherwise survive under the new one.
func repaint() tea.Cmd { return tea.ClearScreen }

// acceptPick carries out the picker's primary action on the selection.
func (m Model) acceptPick() (Model, tea.Cmd) {
	// A set applies whole, even when empty.
	if m.picker.spec.multi {
		return m.acceptCheckedPick(m.picker.checkedValues())
	}
	item, ok := m.picker.selected()
	if !ok {
		return m.closePicker(), nil
	}
	if mdl, cmd, handled := m.acceptConversationPick(item); handled {
		return mdl, cmd
	}
	return m.acceptSettingPick(item)
}

func (m Model) acceptCheckedPick(values []string) (Model, tea.Cmd) {
	switch m.picker.kind {
	case pickerRoomSpaces:
		return m.applyRoomSpaces(values)
	case pickerDictionaries:
		return m.acceptDictionaryOffer(values)
	case pickerFrequencies:
		return m.acceptFrequencyOffer(values)
	case pickerCompletionModel:
		return m.acceptModelOffer(values)
	default:
		return m.closePicker(), nil
	}
}

// acceptConversationPick handles pickers that act on what is being read; handled is
// false for the setting pickers.
func (m Model) acceptConversationPick(item pickerItem) (Model, tea.Cmd, bool) {
	switch m.picker.kind {
	case pickerReaction:
		return answered(m.reactWithPicked(item.value))
	case pickerEmoji:
		return answered(m.insertPickedEmoji(item.value))
	case pickerPeople:
		return answered(m.aliasSelectedPerson())
	case pickerContext:
		return answered(m.acceptContext(m.picker.then, item.value))
	case pickerThread:
		return answered(m.openPickedThread(domain.EventID(item.value), item.room))
	case pickerJump:
		return answered(m.acceptJump(item.value))
	case pickerScheduled:
		// The queue is re-read after canceling, so it shows what the daemon holds.
		return m.closePicker(), m.cancelScheduledCmd(item.value), true
	default:
		return m, nil, false
	}
}

// acceptSettingPick handles pickers that write a setting.
func (m Model) acceptSettingPick(item pickerItem) (Model, tea.Cmd) {
	switch m.picker.kind {
	case pickerIdentity:
		return m.chooseIdentity(item.value)
	case pickerColor:
		return m.chooseColor(item.value)
	case pickerHidden:
		return m.unhideGroup(item.value)
	case pickerRuleScope, pickerRuleList:
		return m.chooseRuleScope(atoiSafe(item.value))
	case pickerSpeedScope:
		return m.chooseSpeedScope(atoiSafe(item.value))
	case pickerRulePreset:
		return m.chooseRulePreset(item.value)
	case pickerSetting:
		return m.chooseSetting(item.value)
	case pickerSettingValue:
		return m.chooseSettingValue(item.value)
	case pickerDNDScope, pickerMuteScope:
		return m.chooseDNDScope(atoiSafe(item.value))
	case pickerDNDFor:
		return m.chooseDNDFor(item.value)
	case pickerNewRoom:
		return m.chooseNewRoomKind(item.value)
	case pickerTags:
		return m.chooseTag(item.value)
	case pickerTagEdit:
		return m.chooseTagRow(item.value)
	case pickerTagEntries:
		return m.chooseTagEntry(item.value)
	case pickerNone:
		return m, nil
	default:
		return m.closePicker(), nil
	}
}

// pickerHint is the open picker's legend, which depends on the mode: while
// filtering, letters are text, so only non-typing keys are advertised.
func (m Model) pickerHint() string {
	var rename []hint
	if m.picker.kind == pickerThread {
		rename = []hint{keyed(m.keys.keyHint(scopePicker, actRenameThread), "name it")}
	}
	if m.picker.mode == pickerFilter {
		closeLabel := "close"
		if m.picker.spec.modal {
			closeLabel = "stop filtering"
		}
		return m.hintLine(append([]hint{
			note("type to filter"),
			keyed(m.keys.commandKeyHint(scopeNav, actUp)+"/"+m.keys.commandKeyHint(scopeNav, actDown), "move"),
			keyed(m.keys.commandKeyHint(scopeNav, actOpen), "next"),
			keyed(m.keys.commandKeyHint(scopePicker, actAcceptPick), "choose"),
			keyed(m.keys.commandKeyHint(scopePicker, actClosePick), closeLabel),
		}, rename...)...)
	}
	if m.picker.spec.multi {
		return m.hintLine(
			keyed(m.keys.keyHint(scopePicker, actTogglePick), "tick"),
			keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "move"),
			keyed(m.keys.keyHint(scopePicker, actAcceptPick), "apply"),
			keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
			keyed(m.keys.keyHint(scopePicker, actClosePick), "cancel"),
		)
	}
	return m.hintLine(append([]hint{
		keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "move"),
		keyed(m.keys.keyHint(scopePicker, actAcceptPick), "choose"),
		keyed(m.keys.keyHint(scopePicker, actClosePick), "close"),
	}, rename...)...)
}
