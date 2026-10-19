package tui

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// The phonebook's cards, held so drawing and searching never read the folder: read again
// when the config is applied, after kith writes a card, when :people or a card opens, and
// when the room list refreshes. A place's show_field puts a card field after names in its
// room list and member list; its rooms' members are read to find each room's one person.

// cardsState is the cards as last read, and the members read for show_field.
type cardsState struct {
	index phonebook.Index
	// asked counts reads issued and read the one applied, so an older read landing later
	// is dropped.
	asked, read uint64
	// members is each room's members as read for show_field, with the room's shape then.
	members map[domain.RoomID]shapedMembers
	// asking is the shape each room's members were last asked for.
	asking map[domain.RoomID]string
}

// shapedMembers is a room's members and the room's shape (roomShape) they were read at.
type shapedMembers struct {
	shape   string
	members []domain.Member
}

// cardsMsg is the folder's cards as read by read number seq; open opens :people on them.
type cardsMsg struct {
	seq   uint64
	dir   string
	cards []phonebook.Card
	err   error
	open  bool
	terms string
}

// loadCards reads the folder's cards off the Update; open opens :people filtered by terms.
func (m Model) loadCards(open bool, terms string) (Model, tea.Cmd) {
	m.cards.asked++
	seq, dir, book := m.cards.asked, m.prefs.phonebookDir, m.book()
	return m, func() tea.Msg {
		cards, err := book.Cards()
		return cardsMsg{seq: seq, dir: dir, cards: cards, err: err, open: open, terms: terms}
	}
}

// handleCards keeps the newest read of the current folder, and opens :people when asked.
func (m Model) handleCards(msg cardsMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		if msg.open {
			return m.sayErr("could not read the phonebook", msg.err), nil
		}
		m.logErr(slog.LevelDebug, "read the phonebook", msg.err)
		return m, nil
	}
	var cmd tea.Cmd
	if msg.dir == m.prefs.phonebookDir && msg.seq > m.cards.read {
		m.cards.index, m.cards.read = phonebook.NewIndex(msg.cards), msg.seq
		m = m.refreshCardPickers()
		if m.search.active && domain.ParseSearch(m.search.query, time.Now()).SenderField != "" {
			m, cmd = m.runSearch(m.search.query)
		}
	}
	if msg.open {
		return m.showPhonebook(msg.terms), cmd
	}
	return m, cmd
}

// refreshCardPickers rebuilds an open list that shows cards, keeping its filter and row.
func (m Model) refreshCardPickers() Model {
	var items []pickerItem
	switch m.picker.kind {
	case pickerPeople:
		items = m.peopleItems()
	case pickerJump:
		items = m.jumpItems()
	default:
		return m
	}
	at, _ := m.picker.selected()
	next := newPickerWith(m.picker.kind, m.picker.spec, items)
	next.mode, next.filter = m.picker.mode, m.picker.filter
	m.picker = next.refilter().at(at.value)
	return m
}

// cardOf is id's person's card, as last read.
func (m Model) cardOf(id string) (phonebook.Card, bool) {
	return m.cards.index.Find(m.personIdentifiers(id))
}

// cardAt is the card read from path.
func (m Model) cardAt(path string) (phonebook.Card, bool) {
	for _, card := range m.cards.index.Cards() {
		if card.Path == path {
			return card, true
		}
	}
	return phonebook.Card{}, false
}

// besideName is name with the rail place's show_field value from id's card after it.
func (m Model) besideName(name, id string) string {
	key := m.prefs.display.ShowFieldIn(m.namesHome(m.rail.key()))
	if key == "" || id == "" {
		return name
	}
	card, ok := m.cardOf(id)
	if !ok {
		return name
	}
	values := card.Field(key)
	if len(values) == 0 {
		return name
	}
	return isolate(name) + " · " + isolate(strings.Join(values, ", "))
}

// roomPerson is the room's one person besides you and bridge bots, from members read for
// show_field or for its name, or the open room's.
func (m Model) roomPerson(room domain.Room) (string, bool) {
	read, ok := m.cards.members[room.ID]
	members := read.members
	if !ok {
		members, ok = m.peers.members[room.ID]
	}
	if !ok && room.ID == m.openRoom {
		members, ok = m.timeline.members, len(m.timeline.members) > 0
	}
	if !ok {
		return "", false
	}
	peer, many := m.onePerson(members)
	return peer.UserID, !many && peer.UserID != ""
}

// onePerson is the one member who is neither you nor a bridge bot; many is whether there
// are more, or the list was cut at peerMembersLimit.
func (m Model) onePerson(members []domain.Member) (domain.Member, bool) {
	if len(members) >= peerMembersLimit {
		return domain.Member{}, true
	}
	var peer domain.Member
	for _, member := range members {
		if m.isMe(member.UserID) || domain.IsBridgeBot(member.UserID) {
			continue
		}
		if peer.UserID != "" {
			return domain.Member{}, true
		}
		peer = member
	}
	return peer, false
}

// roomShape changes when what decides a room's one person may have.
func roomShape(room domain.Room) string {
	return room.Name + "\x00" + strings.Join(room.Members, "\x00")
}

// fieldMembersMsg is the members read for show_field, by room; failed are the rooms whose
// read failed, asked again at the next refresh.
type fieldMembersMsg struct {
	members map[domain.RoomID]shapedMembers
	failed  []failedRead
}

// failedRead is a room whose members could not be read at a shape.
type failedRead struct {
	id    domain.RoomID
	shape string
}

// refreshFieldMembers reads the members of every room listed in a place with show_field
// whose shape changed since they were read or asked for.
func (m Model) refreshFieldMembers() (Model, tea.Cmd) {
	view := m.unreadView()
	// want is each room to read, by the shape it is read at.
	want := map[domain.RoomID]string{}
	for _, g := range m.rail.groups {
		if m.prefs.display.ShowFieldIn(m.namesHome(g.key)) == "" {
			continue
		}
		for i := range m.rooms.all {
			room := m.rooms.all[i]
			if shape := roomShape(room); !room.IsInvite() && g.admits(view, room) && m.cards.asking[room.ID] != shape {
				want[room.ID] = shape
			}
		}
	}
	if len(want) == 0 {
		return m, nil
	}
	asking := maps.Clone(m.cards.asking)
	if asking == nil {
		asking = map[domain.RoomID]string{}
	}
	maps.Copy(asking, want)
	m.cards.asking = asking
	ctx, backend := m.ctx, m.backend
	return m, func() tea.Msg {
		msg := fieldMembersMsg{members: make(map[domain.RoomID]shapedMembers, len(want))}
		for _, id := range slices.Sorted(maps.Keys(want)) {
			list, err := backend.Members(ctx, id, peerMembersLimit)
			if err != nil {
				msg.failed = append(msg.failed, failedRead{id: id, shape: want[id]})
				continue
			}
			msg.members[id] = shapedMembers{shape: want[id], members: list}
		}
		return msg
	}
}

// handleFieldMembers keeps the members read for the shape last asked; an older read
// landing later is dropped, and a failed room is asked again next time.
func (m Model) handleFieldMembers(msg fieldMembersMsg) (Model, tea.Cmd) {
	members := maps.Clone(m.cards.members)
	if members == nil {
		members = map[domain.RoomID]shapedMembers{}
	}
	for id, read := range msg.members {
		if m.cards.asking[id] == read.shape {
			members[id] = read
		}
	}
	m.cards.members = members
	if len(msg.failed) > 0 {
		asking := maps.Clone(m.cards.asking)
		for _, failed := range msg.failed {
			if asking[failed.id] == failed.shape {
				delete(asking, failed.id)
			}
		}
		m.cards.asking = asking
	}
	return m, nil
}

// knownPeople is every person ID the client holds: the directory's, identities', DM and
// `from:` candidates', and every member read.
func (m Model) knownPeople() []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] && domain.IsUserID(id) {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, n := range m.dir.dir.Names() {
		add(n.ID)
	}
	for _, l := range m.dir.dir.Links() {
		add(l.ID)
		add(l.Other)
	}
	for _, id := range slices.Sorted(maps.Keys(m.prefs.identities)) {
		add(id)
	}
	for _, list := range [][]domain.Member{m.dmCandidates, m.search.people, m.timeline.members} {
		for _, p := range list {
			add(p.UserID)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.cards.members)) {
		for _, p := range m.cards.members[id].members {
			add(p.UserID)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.peers.members)) {
		for _, p := range m.peers.members[id] {
			add(p.UserID)
		}
	}
	return out
}

// cardPeople is every known ID, not yours, whose person is the person on card.
func (m Model) cardPeople(card phonebook.Card) []string {
	var out []string
	for _, id := range append(m.knownPeople(), card.IDs["matrix"]...) {
		if m.isMe(id) || slices.Contains(out, id) {
			continue
		}
		if found, ok := m.cardOf(id); ok && found.Path == card.Path {
			out = append(out, id)
		}
	}
	return out
}

// fieldSenders is every ID of each person whose card meets the field term (from:key:value),
// or why there is none.
func (m Model) fieldSenders(term string) ([]string, string) {
	t, ok := phonebook.ParseTerm(term)
	if !ok {
		return nil, "from: takes a name, an ID, or key:value"
	}
	q := phonebook.Query{Terms: []phonebook.Term{t}}
	met := map[string]bool{}
	var ids []string
	for _, card := range m.cards.index.Cards() {
		if card.Matches(q) {
			met[card.Path] = true
			ids = append(ids, card.IDs["matrix"]...)
		}
	}
	if len(met) == 0 {
		return nil, "no one's card has " + isolate(t.String())
	}
	var out []string
	for _, id := range append(m.knownPeople(), ids...) {
		if card, found := m.cardOf(id); !found || !met[card.Path] {
			continue
		}
		for _, other := range m.personIDs(id) {
			if !slices.Contains(out, other) {
				out = append(out, other)
			}
		}
	}
	if len(out) == 0 {
		return nil, "no one kith knows has a card with " + isolate(t.String())
	}
	return out, ""
}

// openCard reads the card at path and opens it.
func (m Model) openCard(path, name string) (Model, tea.Cmd) {
	book := m.book()
	return m.doing("reading " + isolate(name) + "'s card…"), func() tea.Msg {
		card, err := book.Read(path)
		return personCardMsg{name: name, card: card, found: err == nil, err: err}
	}
}

// openPhonebook is :people: every card, filtered by terms, read afresh.
func (m Model) openPhonebook(terms string) (Model, tea.Cmd) {
	return m.loadCards(true, terms)
}

// showPhonebook lists the cards by name, filtered by terms (phonebook.Query).
func (m Model) showPhonebook(terms string) Model {
	cards := m.cards.index.Cards()
	if len(cards) == 0 {
		return m.say("the phonebook has no cards yet — I on a person makes one")
	}
	items := make([]pickerItem, 0, len(cards))
	for i := range cards {
		items = append(items, pickerItem{
			label:  isolate(cards[i].Name),
			detail: isolate(strings.Join(cards[i].Field("aliases"), ", ")),
			value:  cards[i].Path,
			card:   &cards[i],
		})
	}
	m.picker = newPicker(pickerCards, items)
	m.picker.filter = terms
	m.picker = m.picker.refilter()
	return m
}

// choosePhonebookCard opens the chosen card.
func (m Model) choosePhonebookCard(item pickerItem) (Model, tea.Cmd) {
	m = m.closePicker()
	return m.openCard(item.value, stripIsolates(item.label))
}

// cardRoomsMsg is the rooms the people on a card are in, newest first, with the members
// of those that are not DMs.
type cardRoomsMsg struct {
	path    string
	name    string
	ids     []string
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	err     error
}

// personRoomsLimit bounds the rooms looked at per ID for a conversation with someone.
const personRoomsLimit = 10

// goToCard goes to the conversation with the person on the card at path: a DM or a room
// with them alone, else a new DM where one can start, else their card.
func (m Model) goToCard(path string) (Model, tea.Cmd) {
	m = m.closePicker()
	card, ok := m.cardAt(path)
	if !ok {
		return m.say("that card is no longer in the phonebook"), nil
	}
	ids := m.cardPeople(card)
	if len(ids) == 0 {
		return m.openCard(path, card.Name)
	}
	ctx, backend := m.ctx, m.backend
	return m.doing("finding your conversation with " + isolate(card.Name) + "…"), func() tea.Msg {
		msg := cardRoomsMsg{path: path, name: card.Name, ids: ids, members: map[domain.RoomID][]domain.Member{}}
		for _, id := range ids {
			rooms, err := backend.RoomsWith(ctx, []string{id}, domain.EveryRoom(), personRoomsLimit)
			if err != nil {
				msg.err = err
				return msg
			}
			for i := range rooms {
				room := &rooms[i]
				if slices.ContainsFunc(msg.rooms, func(r domain.Room) bool { return r.ID == room.ID }) {
					continue
				}
				if !room.IsDirect {
					members, err := backend.Members(ctx, room.ID, peerMembersLimit)
					if err != nil {
						msg.err = err
						return msg
					}
					msg.members[room.ID] = members
				}
				msg.rooms = append(msg.rooms, *room)
			}
		}
		return msg
	}
}

// handleCardRooms opens the first listed DM or one-person room with them, else starts a
// DM with a candidate who is them, else opens their card.
func (m Model) handleCardRooms(msg cardRoomsMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not look for "+isolate(msg.name)+"'s rooms", msg.err), nil
	}
	m = m.clearStatus()
	for i := range msg.rooms {
		room := &msg.rooms[i]
		if room.IsInvite() || indexOfRoom(m.rooms.all, room.ID) < 0 {
			continue
		}
		peer, many := m.onePerson(msg.members[room.ID])
		if room.IsDirect || (!many && slices.Contains(msg.ids, peer.UserID)) {
			return m.goToRoom(room.ID)
		}
	}
	for _, person := range m.dmCandidates {
		if slices.Contains(msg.ids, person.UserID) {
			return m.startDirect(person.UserID)
		}
	}
	return m.openCard(msg.path, msg.name)
}
