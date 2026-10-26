package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// A picker is a modal chooser over a list. One with letter actions (e.g. `a` to alias a
// person) starts in navigate mode; `i` enters filter mode and esc steps back. One whose only
// action is choose filters from the first keystroke.

// pickerWalk is the state of a chooser walk beyond its items.
type pickerWalk struct {
	// identity is the identity edit being walked through (picker, then prompt).
	identity pendingIdentity
	// setting is the preference whose value is being chosen; settingGroup is its settings group
	// (settings.go).
	setting      string
	settingGroup string
	// settingEntry is the entry of a list setting being typed; -1 adds one.
	settingEntry int
	// speedScopes is where a voice-note speed may be remembered, by picker row.
	speedScopes []ruleTarget
	// tag is where the tag editor is (tageditor.go).
	tag tagEditing
	// filter is the filter being added, written at its last step (filteradd.go); filterBack
	// is the place page it began on, "" for the Filters list.
	filter     config.Filter
	filterBack string
	// secret is a command's secrets being asked for (cmdtemplate.go).
	secret secretWalk
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
	pickerRuleScope
	pickerSpeedScope
	pickerRuleList
	pickerRulePreset
	pickerSettingGroups
	pickerSetting
	pickerSettingValue
	pickerSettingEntries
	pickerSettingSet // a set setting's choices, ticked
	pickerDNDScope
	pickerMuteScope
	pickerDNDFor
	pickerScheduled // pending scheduled messages; choosing one cancels it
	pickerThread
	pickerRoomSpaces
	pickerVote      // one answer of a poll
	pickerVoteMulti // any answers of a poll that takes several
	pickerDictionaries
	pickerFrequencies
	pickerCompletionModel
	pickerNewRoom
	pickerPerson       // a person's phonebook card: its fields, and a row to add one
	pickerPersonKeys   // which field to add to a card
	pickerNewMembers   // who is in a chat being made on another network
	pickerJump         // the switcher; see jump.go
	pickerLoginNetwork // :login's networks; see login.go
	pickerLoginAccount
	pickerImportAccount // :import's accounts, folders and choices; see imports.go
	pickerImportFolders
	pickerImportChoice
	pickerCards           // :people, every card in the phonebook; see cards.go
	pickerRescan          // the moves a rescan would make; see rescan.go
	pickerFilterTag       // the tag a new filter moves rooms into; see filteradd.go
	pickerFilterPlaces    // where it watches
	pickerFilterMoves     // what its catch moves a room by
	pickerCommandTemplate // a command to add from a template; see cmdtemplate.go
	pickerIgnored         // the MXIDs this account ignores on Matrix; see ignored.go
	pickerPublicRooms     // Matrix public rooms directory; see publicrooms.go
	pickerChooseAccount   // which account a call naming none goes to; see accountpick.go
	pickerProfile         // the profiles to switch to; see profiles.go
)

// pickerSpec is a kind's fixed properties, copied into the picker when it opens.
type pickerSpec struct {
	title string
	// modal marks a picker with letter actions, which starts in navigate mode.
	modal bool
	// grid renders items as a grid of values (emoji) rather than labeled rows.
	grid bool
	// multi makes a checkbox set: space ticks, enter applies the set. It implies modal, since space
	// is text in filter mode.
	multi bool
}

var pickerSpecs = map[pickerKind]pickerSpec{
	pickerEmoji:           {title: "Emoji", grid: true},
	pickerReaction:        {title: "React with", grid: true},
	pickerPeople:          {title: "People in this room", modal: true},
	pickerIdentity:        {title: "Add to which person?"},
	pickerColor:           {title: "Color"},
	pickerContext:         {}, // title supplied at the call site
	pickerScheduled:       {title: "Scheduled — choose one to cancel, or a failing one to try again", modal: true},
	pickerRuleScope:       {title: "Notifications for what?"},
	pickerSpeedScope:      {title: "Play at this speed for what?"},
	pickerRuleList:        {title: "Notification rules", modal: true},
	pickerRulePreset:      {title: "Notify how?"},
	pickerSettingGroups:   {title: "Settings", modal: true},
	pickerSetting:         {modal: true},              // titled with the group
	pickerSettingEntries:  {modal: true},              // titled with the setting
	pickerSettingSet:      {modal: true, multi: true}, // titled with the setting
	pickerSettingValue:    {title: "Set it to"},
	pickerDNDScope:        {title: "Do not disturb for what?"},
	pickerMuteScope:       {title: "Mute sound for what?"},
	pickerDNDFor:          {title: "For how long?"},
	pickerThread:          {title: "Threads in this room"},
	pickerRoomSpaces:      {title: "Which spaces and tags hold this room?", modal: true, multi: true},
	pickerVote:            {modal: true},              // titled with the question
	pickerVoteMulti:       {modal: true, multi: true}, // titled with the question
	pickerDictionaries:    {title: "Install spelling dictionaries for what you write?", modal: true, multi: true},
	pickerFrequencies:     {title: "Install word counts, so typos that are also words get caught?", modal: true, multi: true},
	pickerCompletionModel: {title: "Install the completion model, to run on this machine?", modal: true, multi: true},
	pickerNewRoom:         {title: "Create what?"},
	pickerPerson:          {modal: true},              // titled with the person
	pickerPersonKeys:      {},                         // titled with the person
	pickerNewMembers:      {modal: true, multi: true}, // titled with the chat's name
	pickerJump:            {title: "Go to"},
	pickerLoginNetwork:    {title: "Sign in to what?"},
	pickerLoginAccount:    {}, // titled with the network
	pickerImportAccount:   {title: "Copy folders from which account?"},
	pickerImportFolders:   {}, // titled with the account
	pickerImportChoice:    {}, // titled with the folder
	pickerCards:           {title: "People in the phonebook"},
	pickerRescan:          {modal: true, multi: true}, // titled with the count
	pickerFilterTag:       {title: "Move rooms into which tag?"},
	pickerFilterPlaces:    {title: "Watch where? None ticked is everywhere", modal: true, multi: true},
	pickerFilterMoves:     {title: "Move a room, or mark its sender, into Spam when it catches", modal: true, multi: true},
	pickerCommandTemplate: {title: "Add which command?"},
	pickerIgnored:         {title: "Ignored on Matrix", modal: true},
	pickerPublicRooms:     {title: "Matrix public rooms — enter joins", modal: true},
	pickerChooseAccount:   {}, // titled with the network
	pickerProfile:         {title: "Switch to which profile?"},
}

// pickerItem is one row: label shown, detail dimmed, value acted on, and match the filter text
// (defaults to label+detail).
type pickerItem struct {
	label  string
	detail string
	value  string
	match  string
	// room is where the row's thing lives, for lists spanning rooms; empty means the open room.
	room domain.RoomID
	// card matches the row by a person's card (phonebook.Card.Matches) instead of its text;
	// byTerms hides it until a field term is typed.
	card    *phonebook.Card
	byTerms bool
}

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
	// checked is the ticked rows of a multi picker, keyed by value so filtering cannot move ticks.
	checked map[string]bool
}

func (p picker) active() bool { return p.kind != pickerNone }

func (p picker) ticked(value string) bool { return p.checked[value] }

// at puts the cursor on the row with value, else on the first row.
func (p picker) at(value string) picker {
	for i, item := range p.items {
		if item.value == value {
			p.cursor = i
			break
		}
	}
	return p
}

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

// refilter recomputes the visible items: prefix matches first, then substring, as in the
// mention and emoji completions, then the rows matched by their cards.
func (p picker) refilter() picker {
	query := strings.ToLower(strings.TrimSpace(p.filter))
	terms := phonebook.ParseQuery(p.filter)
	var prefix, contains, cards []pickerItem
	for _, item := range p.all {
		if item.card != nil {
			if (!item.byTerms || terms.HasTerms()) && item.card.Matches(terms) {
				cards = append(cards, item)
			}
			continue
		}
		text := strings.ToLower(item.matchText())
		switch {
		case strings.HasPrefix(text, query):
			prefix = append(prefix, item)
		case strings.Contains(text, query):
			contains = append(contains, item)
		}
	}
	p.items = slices.Concat(prefix, contains, cards)
	p.cursor = clampIndex(p.cursor, len(p.items))
	return p
}

// matchText is what filtering matches, with label isolate marks stripped (they are not typed).
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
	// Closing replaces a full pane; repaint so mis-measured glyph cells from the picker do not
	// survive.
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
		// Only edits that keep the caret at the end: arrows walk the list.
		if ed, ok := m.editorFor(fieldFilter).editTail(key, m.keys); ok {
			return m.store(fieldFilter, ed), nil, true
		}
	}
	switch m.keys.lookup(key.String(), scopePicker) {
	case actFilter:
		m.picker.mode = pickerFilter
		return m, nil, true
	case actRenameThread:
		// Handled here, not in pickerAction: the thread list always filters, and this binding is a
		// chord, which produces no text.
		if m.picker.kind == pickerThread {
			return answered(m.renameSelectedThread())
		}
	case actTogglePick:
		m.picker = m.picker.tick()
		return m, nil, true
	case actAcceptPick:
		return answered(m.acceptPick())
	case actClosePick:
		// esc first leaves filter mode (modal) or clears the query (non-modal), then closes.
		if m.picker.spec.modal && m.picker.mode == pickerFilter {
			m.picker.mode = pickerNavigate
		} else if m.picker.filter == "" {
			if back := pickerActions[m.picker.kind].back; back != nil {
				return back(m), nil, true
			}
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

// move is the cursor arithmetic for one motion. Grids step vertically by a row's width; a page
// is the visible rows (navDelta). A count multiplies steps, not pages; with the ends it names a
// row (vim's `12G`).
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
	if m.picker.mode != pickerNavigate {
		return m, nil, false
	}
	if m.keys.lookup(key.String(), scopePicker) == actRemoveEntry {
		if mdl, cmd, handled := m.removeUnderCursor(); handled {
			return mdl, cmd, true
		}
	}
	if m.picker.kind == pickerSetting {
		switch m.keys.lookup(key.String(), scopePicker) {
		case actIncrease:
			return answered(m.stepSetting(1))
		case actDecrease:
			return answered(m.stepSetting(-1))
		default:
			return m, nil, false
		}
	}
	if m.picker.kind == pickerSettingEntries {
		switch m.keys.lookup(key.String(), scopePicker) {
		case actMoveEntryUp:
			return answered(m.moveSettingEntry(-1))
		case actMoveEntryDown:
			return answered(m.moveSettingEntry(1))
		default:
			return m, nil, false
		}
	}
	if m.picker.kind != pickerPeople {
		return m, nil, false
	}
	switch m.keys.lookup(key.String(), scopePicker) {
	case actKick:
		return answered(m.askKickSelected())
	case actBan:
		return answered(m.askBanSelected())
	case actName:
		return answered(m.aliasSelectedPerson())
	case actPerson:
		return answered(m.personOfMember())
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

// repaint clears and redraws the screen, used where a full pane is replaced: when program and
// terminal disagree on a glyph's width, old cells otherwise survive.
func repaint() tea.Cmd { return tea.ClearScreen }

// acceptPick carries out the picker's primary action on the selection.
func (m Model) acceptPick() (Model, tea.Cmd) {
	act := pickerActions[m.picker.kind]
	// A set applies whole, even when empty.
	if m.picker.spec.multi {
		if act.apply == nil {
			return m.closePicker(), nil
		}
		return act.apply(m, m.picker.checkedValues())
	}
	item, ok := m.picker.selected()
	if !ok || act.choose == nil {
		return m.closePicker(), nil
	}
	return act.choose(m, item)
}

// acceptConversationPick handles pickers acting on what is being read; handled is false for

// pickerHint is the picker legend; while filtering, letters are text, so only non-typing keys
// are shown.
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
	if hints, ok := m.settingsHint(); ok {
		return hints
	}
	return m.hintLine(append([]hint{
		keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
		keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "move"),
		keyed(m.keys.keyHint(scopePicker, actAcceptPick), "choose"),
		keyed(m.keys.keyHint(scopePicker, actClosePick), "close"),
	}, rename...)...)
}

// settingsHint is a settings level's legend: what enter does on the row, the number keys on a
// number, and esc stepping back. false elsewhere.
func (m Model) settingsHint() (string, bool) {
	move := keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "move")
	switch m.picker.kind {
	case pickerSettingGroups:
		return m.hintLine(move,
			keyed(m.keys.keyHint(scopePicker, actAcceptPick), "open"),
			keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
			keyed(m.keys.keyHint(scopePicker, actClosePick), "close")), true
	case pickerSetting:
		hints := []hint{move, keyed(m.keys.keyHint(scopePicker, actAcceptPick), "change")}
		if item, ok := m.picker.selected(); ok {
			if s, ok := m.setting(item.value); ok && s.kind == settingNumber {
				hints = append(hints, keyed(m.keys.keyHint(scopePicker, actIncrease)+"/"+m.keys.keyHint(scopePicker, actDecrease), "step"))
			}
		}
		return m.hintLine(append(hints,
			keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
			keyed(m.keys.keyHint(scopePicker, actClosePick), "back"))...), true
	case pickerSettingValue:
		return m.hintLine(move,
			keyed(m.keys.keyHint(scopePicker, actAcceptPick), "set"),
			keyed(m.keys.keyHint(scopePicker, actClosePick), "back")), true
	case pickerSettingEntries:
		return m.hintLine(move,
			keyed(m.keys.keyHint(scopePicker, actAcceptPick), "edit"),
			keyed(m.keys.keyHint(scopePicker, actRemoveEntry), "remove"),
			keyed(m.keys.keyHint(scopePicker, actMoveEntryUp)+"/"+m.keys.keyHint(scopePicker, actMoveEntryDown), "move it"),
			keyed(m.keys.keyHint(scopePicker, actFilter), "filter"),
			keyed(m.keys.keyHint(scopePicker, actClosePick), "back")), true
	default:
		return "", false
	}
}
