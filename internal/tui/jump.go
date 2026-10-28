package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The switcher (ctrl+k): type part of a person, room or space name and go there, over lists
// already held. People with no room yet come last as "start a DM" rows, so no row looks like a
// place but creates one. A field term (role:attorney) lists the people whose phonebook card
// meets it.

// jumpItems is everything the switcher reaches: unread rooms, other rooms, DM candidates, rail
// groups, then the phonebook's people, shown only for a field term.
func (m Model) jumpItems() []pickerItem {
	view := m.unreadView()
	items := make([]pickerItem, 0, len(m.rooms.all)+len(m.rail.groups))
	for _, unreadFirst := range []bool{true, false} {
		for i := range m.rooms.all {
			room := m.rooms.all[i]
			count, _ := view.count(room)
			if (count > 0) != unreadFirst {
				continue
			}
			items = append(items, m.jumpRoomItem(view, room, count))
		}
	}
	for _, person := range m.dmCandidates {
		items = append(items, pickerItem{
			label:  isolate(m.byNumber(person.DisplayName)),
			detail: "start a DM",
			value:  "dm:" + person.UserID,
			// The MXID matches too: you may know only their address or number.
			match: m.byNumber(person.DisplayName) + " " + person.DisplayName + " " + person.UserID,
		})
	}
	for _, g := range m.rail.groups {
		items = append(items, pickerItem{
			label:  isolate(g.label),
			detail: "space",
			value:  "group:" + g.key,
			// The key matches too, so a renamed group is findable by its Matrix name.
			match: g.label + " " + g.key,
		})
	}
	cards := m.cards.index.Cards()
	for i := range cards {
		items = append(items, pickerItem{
			label:   isolate(cards[i].Name),
			detail:  "person",
			value:   "card:" + cards[i].Path,
			card:    &cards[i],
			byTerms: true,
		})
	}
	return items
}

// jumpRoomItem is one room's row: what it is called here, and what it is.
func (m Model) jumpRoomItem(view unreadView, room domain.Room, unread int) pickerItem {
	item := pickerItem{
		label: m.roomName(room),
		value: "room:" + string(room.ID),
		// The room ID is matchable but not shown, to tell same-named rooms apart.
		match: m.roomLabel(room) + " " + strings.Join(room.Members, " ") + " " + string(room.ID),
	}
	item.detail = jumpDetail(room, unread, view.ownerName(room))
	return item
}

// jumpDetail says what a row is: invitation or unread count, then DM or the exclusive tag it
// shows under.
func jumpDetail(room domain.Room, unread int, owner string) string {
	parts := make([]string, 0, 2)
	switch {
	case room.IsInvite():
		parts = append(parts, "invitation")
	case unread > 0:
		parts = append(parts, strconv.Itoa(unread)+" unread")
	}
	switch {
	case room.IsDirect:
		parts = append(parts, "DM")
	case owner != "":
		parts = append(parts, owner)
	}
	return strings.Join(parts, " · ")
}

// openJump opens the switcher. It works in every mode, including typing; insert mode is kept so
// backing out returns to the sentence.
func (m Model) openJump() (Model, tea.Cmd) {
	items := m.jumpItems()
	if len(items) == 0 {
		return m.say("no rooms yet"), nil
	}
	m.picker = newPicker(pickerJump, items)
	// DM candidates are fetched on open; only this screen wants them.
	return m, tea.Batch(repaint(), m.directCandidatesCmd())
}

// handleDirectCandidates folds in DM candidates and rebuilds the open switcher, keeping the
// filter.
func (m Model) handleDirectCandidates(msg directCandidatesMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		// Not reported: the switcher works without these rows.
		return m, nil
	}
	m.dmCandidates = msg.people
	if m.picker.kind != pickerJump {
		return m, nil
	}
	filter := m.picker.filter
	m.picker = newPicker(pickerJump, m.jumpItems())
	m.picker.filter = filter
	m.picker = m.picker.refilter()
	return m, repaint()
}

// startDirect creates an encrypted DM and opens it. No encryption question: a two-person room
// cannot be encrypted too soon, and encryption is only chosen at creation.
func (m Model) startDirect(user string) (Model, tea.Cmd) {
	m = m.closePicker()
	if user == "" {
		return m, nil
	}
	name := user
	for _, person := range m.dmCandidates {
		if person.UserID == user && person.DisplayName != "" {
			name = m.byNumber(person.DisplayName)
			break
		}
	}
	return m.pickAccount(accountThen{kind: thenCreate, network: domain.NetworkOf(user), name: name, spec: domain.NewRoom{
		// No m.room.name: it would override every client's member-derived DM label.
		Encrypted: true,
		Direct:    true,
		Invite:    []string{user},
	}})
}

// acceptJump goes where a row says: "kind:id", split once (IDs contain colons).
func (m Model) acceptJump(value string) (Model, tea.Cmd) {
	kind, target, ok := strings.Cut(value, ":")
	if !ok {
		return m.closePicker(), nil
	}
	switch kind {
	case "group":
		return m.jumpToGroup(target)
	case "dm":
		return m.startDirect(target)
	case "card":
		return m.goToCard(target)
	}
	return m.goToRoom(domain.RoomID(target))
}

// goToRoom opens a room from anywhere, first moving the rail to a group holding it so the
// room-list highlight stays on the open room.
func (m Model) goToRoom(id domain.RoomID) (Model, tea.Cmd) {
	m = m.closePicker()
	idx := indexOfRoom(m.rooms.all, id)
	if idx < 0 {
		// Left or archived while the chooser was open.
		return m.say("that room is no longer in the list"), nil
	}
	room := m.rooms.all[idx]
	m.rail.cursor = m.groupHolding(room)
	next, cmd := m.selectRoom(room)
	// An invitation has no timeline: keep focus on the list and decision pane.
	if room.IsInvite() {
		next.focus = paneRooms
		return next, cmd
	}
	opened, openCmd := next.openSelectedRoom()
	return opened, tea.Batch(cmd, openCmd)
}

// groupHolding is the rail group to show a room under: the current one if it holds the room,
// then a real space, then any group.
func (m Model) groupHolding(room domain.Room) int {
	view := m.unreadView()
	if g, ok := m.rail.at(); ok && g.admits(view, room) {
		return m.rail.cursor
	}
	for i := range m.rail.groups {
		if isSpaceGroup(m.rail.groups[i].key) && m.rail.groups[i].admits(view, room) {
			return i
		}
	}
	for i := range m.rail.groups {
		if m.rail.groups[i].admits(view, room) {
			return i
		}
	}
	return m.rail.cursor
}

// jumpToGroup moves the rail to a group and opens its first room, as selecting it in the rail
// does.
func (m Model) jumpToGroup(key string) (Model, tea.Cmd) {
	m = m.closePicker()
	idx := indexOfGroup(m.rail.groups, key)
	if idx >= len(m.rail.groups) {
		return m.say("that space is no longer in the rail"), nil
	}
	m.rail.cursor = idx
	m.focus = paneRooms
	return m.selectGroup()
}

// Jump chords: a sequence bound to a place (`g w` for Work), complementing the switcher.

// jumpKey acts on a sequence bound to a place, reporting whether one was. Never while typing.
func (m Model) jumpKey(press string) (Model, tea.Cmd, bool) {
	if m.typing() {
		return m, nil, false
	}
	target, ok := m.keys.jumpFor(press)
	if !ok {
		return m, nil, false
	}
	return answered(m.goTo(target))
}

// goTo goes where a configured target points, or says why it could not.
func (m Model) goTo(target domain.JumpTarget) (Model, tea.Cmd) {
	if target.Kind == domain.JumpHome {
		if key, found := m.groupNamed(target.Name); found {
			return m.jumpToGroup(key)
		}
		return m.say("nothing in the rail is " + target.Name), nil
	}
	if room, found := m.roomNamed(target); found {
		return m.goToRoom(room.ID)
	}
	return m.say("no room here with the ID " + target.Name), nil
}

// roomNamed is the room a target names. Targets are room IDs, never display names: one person
// over three bridges is three rooms with the same name.
func (m Model) roomNamed(target domain.JumpTarget) (domain.Room, bool) {
	if idx := indexOfRoom(m.rooms.all, domain.RoomID(target.Name)); idx >= 0 {
		return m.rooms.all[idx], true
	}
	return domain.Room{}, false
}

// groupNamed resolves a home entry to the first rail row it names.
func (m Model) groupNamed(entry string) (string, bool) {
	if keys := groupsNamed(m.rail.groups, entry); len(keys) > 0 {
		return keys[0], true
	}
	return "", false
}

// :shortcut binds what you are on: the rail's selected space or tag when the rail has focus,
// else the room; tab in its prompt switches between them. /shortcut is the same from the
// composer, starting on the room written in. Rooms are recorded by ID, which the UI never
// shows, so they are bound by pointing.

// bindShortcut is :shortcut and /shortcut: keys becomes the sequence reaching the focused
// place (bindingTargets). With no keys it asks, prefilled with the place's current one.
func (m Model) bindShortcut(keys string, room *domain.Room) (Model, tea.Cmd) {
	m.compose.input, m.compose.drafted = "", nil
	targets := m.bindingTargets(room)
	if len(targets) == 0 {
		return m.say("nothing to bind a shortcut to — open a room, or select a space or tag"), nil
	}
	m.aimedAt.bindings = targets
	if strings.TrimSpace(keys) == "" {
		return m.askBinding(targets[0])
	}
	m.aimedAt.binding = targets[0]
	return m.submitJumpBinding(keys)
}

// bindingTargets are the places a shortcut can bind to here, focused one first: the rail's
// selected space or tag when the rail has focus, else the room (given, under the room-list
// cursor, or open).
func (m Model) bindingTargets(room *domain.Room) []domain.JumpTarget {
	if room == nil && m.focus == paneRooms {
		if row, ok := m.selectedRow(); ok {
			room = &row.room
		}
	}
	if room == nil {
		if open, ok := m.currentRoom(); ok {
			room = &open
		}
	}
	var roomTarget, placeTarget []domain.JumpTarget
	if room != nil && !room.IsInvite() {
		roomTarget = []domain.JumpTarget{{Kind: domain.JumpRoom, Name: string(room.ID)}}
	}
	if entry, ok := m.currentGroup(); ok && (isSpaceGroup(entry.key) || isTagGroup(entry.key)) {
		placeTarget = []domain.JumpTarget{{Kind: domain.JumpHome, Name: domain.HomeEntry(entry.key)}}
	}
	if m.focus == paneRail {
		return append(placeTarget, roomTarget...)
	}
	return append(roomTarget, placeTarget...)
}

// cycleBindingTarget is tab in the shortcut prompt: the next place, keeping the typed
// sequence.
func (m Model) cycleBindingTarget() (Model, tea.Cmd) {
	if next, ok := m.nextBindingTarget(); ok {
		return m.askBinding(next)
	}
	return m, nil
}

// nextBindingTarget is the place tab moves to; false when there is no other.
func (m Model) nextBindingTarget() (domain.JumpTarget, bool) {
	targets := m.aimedAt.bindings
	if len(targets) < 2 {
		return domain.JumpTarget{}, false
	}
	for i, t := range targets {
		if t == m.aimedAt.binding {
			return targets[(i+1)%len(targets)], true
		}
	}
	return targets[0], true
}

// bindingLabel is the shortcut prompt's lead, naming the place it binds.
func (m Model) bindingLabel() string {
	return "shortcut for " + m.targetName(m.aimedAt.binding) + " (empty unbinds): "
}

// askBinding opens the prompt for a place, prefilled with its current sequence.
func (m Model) askBinding(target domain.JumpTarget) (Model, tea.Cmd) {
	m.aimedAt.binding = target
	return m.openPromptWith(promptJumpBind, m.sequenceBoundTo(target)), nil
}

// sequenceBoundTo is the sequence already pointing at a place, matched on the resolved place,
// not the written text.
func (m Model) sequenceBoundTo(target domain.JumpTarget) string {
	for _, seq := range slices.Sorted(maps.Keys(m.keys.jumps)) {
		if m.samePlace(m.keys.jumps[seq], target) {
			return spellSequence(seq)
		}
	}
	return ""
}

// samePlace reports whether two targets lead to the same place; spaces compare by resolved
// group.
func (m Model) samePlace(a, b domain.JumpTarget) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == domain.JumpHome {
		first, ok := m.groupNamed(a.Name)
		second, also := m.groupNamed(b.Name)
		return ok && also && first == second
	}
	return a.Name == b.Name
}

// submitJumpBinding writes what was typed; an empty sequence unbinds the place.
func (m Model) submitJumpBinding(input string) (Model, tea.Cmd) {
	target := m.aimedAt.binding
	m.aimedAt.binding, m.aimedAt.bindings = domain.JumpTarget{}, nil
	if target.Kind == domain.JumpNone {
		return m, nil
	}
	seq := normalizeSequence(strings.TrimSpace(input))
	if seq == "" {
		return m.applyJumps(m.jumpsWithout(target), "unbound "+m.targetName(target))
	}
	if !sequenceValid(seq) {
		return m.say(fmt.Sprintf("%q is not a key name the terminal reports", input)), nil
	}
	if why := m.keys.jumpConflict(seq); why != "" {
		return m.say(spellSequence(seq) + ": " + why), nil
	}
	// One sequence per place and one place per sequence.
	jumps := append(m.jumpsWithout(target), config.Jump{Chord: seq, Target: target.String()})
	return m.applyJumps(jumps, spellSequence(seq)+" now goes to "+m.targetName(target))
}

// jumpsWithout is the configured list, order kept, minus whatever points at a place.
func (m Model) jumpsWithout(target domain.JumpTarget) config.Jumps {
	jumps := make(config.Jumps, 0, len(m.conf.device.Keys.Jump)+1)
	for _, entry := range m.conf.device.Keys.Jump {
		if existing, ok := domain.ParseJump(entry.Target); ok && m.samePlace(existing, target) {
			continue
		}
		jumps = append(jumps, entry)
	}
	return jumps
}

// applyJumps makes a new jump table the running configuration and writes it.
func (m Model) applyJumps(jumps config.Jumps, done string) (Model, tea.Cmd) {
	dev := m.conf.device.Clone()
	if len(jumps) == 0 {
		// nil so the writer omits the section.
		jumps = nil
	}
	dev.Keys.Jump = jumps
	return m.applyDevice(dev, done)
}

// targetName is what the status line calls a target: its local name.
func (m Model) targetName(target domain.JumpTarget) string {
	if target.Kind == domain.JumpHome {
		if key, ok := m.groupNamed(target.Name); ok {
			return isolate(m.rail.groups[indexOfGroup(m.rail.groups, key)].label)
		}
		return target.Name
	}
	if room, ok := m.roomNamed(target); ok {
		return m.roomName(room)
	}
	return target.Name
}

// jumpBack and jumpForward walk the jumplist, skipping rooms that have gone.
func (m Model) jumpBack() (Model, tea.Cmd) {
	next, to, ok := m.jumps.goingBack(m.roomListed)
	if !ok {
		return m.say("nothing to go back to"), nil
	}
	m.jumps = next
	return m.goToRoom(to)
}

func (m Model) jumpForward() (Model, tea.Cmd) {
	next, to, ok := m.jumps.goingForward(m.roomListed)
	if !ok {
		return m.say("nothing to go forward to"), nil
	}
	m.jumps = next
	return m.goToRoom(to)
}

func (m Model) roomListed(id domain.RoomID) bool {
	return indexOfRoom(m.rooms.all, id) >= 0
}
