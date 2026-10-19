package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// A person's card in the phonebook ([phonebook] dir): the file listing any of their
// identifiers, edited one field at a time. `I` opens it on a message's sender, the member
// under the cursor, or a room's one other person (bots aside). A person with no card gets one
// at the first field written, with every identifier kith knows of theirs.

// personState is the card the person picker is open on, and the field being written.
type personState struct {
	id, name string
	card     phonebook.Card
	found    bool
	// key is the field being written; list whether it is written as a list.
	key  string
	list bool
}

// Non-field rows of the person picker and the key list's new-key row; no key is spelled so.
const (
	personAdd    = "\x00add"
	personNewKey = "\x00new"
	personSaid   = "\x00said"
)

// personCardMsg is a person's card as the folder has it now, and the keys its cards use.
type personCardMsg struct {
	id, name string
	card     phonebook.Card
	found    bool
	err      error
}

// personKeysMsg is every key the folder's cards use, for adding a field.
type personKeysMsg struct {
	keys []string
	err  error
}

// personWrittenMsg is a field written: the card as it is now.
type personWrittenMsg struct {
	card phonebook.Card
	err  error
}

// openPerson reads id's card and opens it.
func (m Model) openPerson(id, name string) (Model, tea.Cmd) {
	if id == "" {
		return m, nil
	}
	book, ids := m.book(), m.personIdentifiers(id)
	m, cards := m.loadCards(false, "")
	return m.doing("reading " + isolate(name) + "'s card…"), tea.Batch(cards, func() tea.Msg {
		card, found, err := book.Find(ids)
		return personCardMsg{id: id, name: name, card: card, found: found, err: err}
	})
}

func (m Model) book() *phonebook.Book { return phonebook.New(m.prefs.phonebookDir) }

// handlePersonCard opens the picker on a card read.
func (m Model) handlePersonCard(msg personCardMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not read the phonebook", msg.err), nil
	}
	m = m.clearStatus()
	m.aimedAt.person = personState{id: msg.id, name: msg.name, card: msg.card, found: msg.found}
	return m.showPerson(""), nil
}

// showPerson lists the card's keys, identifiers first, then a row for what they said and
// an add row, with the cursor on the row valued at.
func (m Model) showPerson(at string) Model {
	p := m.aimedAt.person
	var items []pickerItem
	for _, key := range phonebook.IdentifierKeys {
		if values := p.card.IDs[key]; len(values) > 0 {
			items = append(items, pickerItem{label: key, detail: isolate(strings.Join(values, ", ")), value: key, match: key + " " + strings.Join(values, " ")})
		}
	}
	for _, f := range p.card.Fields {
		items = append(items, pickerItem{label: f.Key, detail: isolate(strings.Join(f.Values, ", ")), value: f.Key, match: f.Key + " " + strings.Join(f.Values, " ")})
	}
	items = append(items,
		pickerItem{label: "Everything they said", detail: "in every room you share", value: personSaid, match: "everything they said messages"},
		pickerItem{label: "Add a field", value: personAdd, match: "add a field"})
	spec := pickerSpecs[pickerPerson]
	spec.title = "About " + p.name + " — no card yet"
	if p.found {
		spec.title = "About " + p.name + " — " + p.card.Name + ".md"
	}
	m.picker = newPickerWith(pickerPerson, spec, items).at(at)
	return m
}

// choosePersonRow edits a field, or asks which to add.
func (m Model) choosePersonRow(value string) (Model, tea.Cmd) {
	if value == personSaid {
		p := m.aimedAt.person
		return m.closePicker().openSaid(p.id, p.name)
	}
	if value == personAdd {
		m = m.closePicker()
		book := m.book()
		return m, func() tea.Msg {
			keys, err := book.Keys()
			return personKeysMsg{keys: keys, err: err}
		}
	}
	values, list := m.aimedAt.person.valuesOf(value)
	m.aimedAt.person.key, m.aimedAt.person.list = value, list
	m = m.openPromptWith(promptPersonValue, strings.Join(values, ", "))
	m.prompt.fresh = true
	return m, nil
}

// valuesOf is a key's values on the card, and whether it is a list.
func (p personState) valuesOf(key string) ([]string, bool) {
	if values, ok := p.card.IDs[key]; ok {
		return values, true
	}
	for _, f := range p.card.Fields {
		if f.Key == key {
			return f.Values, f.List
		}
	}
	return nil, false
}

// handlePersonKeys offers the keys the folder uses that this card has not, and a new one.
func (m Model) handlePersonKeys(msg personKeysMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not read the phonebook", msg.err), nil
	}
	p := m.aimedAt.person
	var items []pickerItem
	for _, key := range msg.keys {
		if _, has := p.valuesOf(key); !has {
			items = append(items, pickerItem{label: key, value: key, match: key})
		}
	}
	items = append(items, pickerItem{label: "New field", detail: "name it", value: personNewKey, match: "new field"})
	spec := pickerSpecs[pickerPersonKeys]
	spec.title = "Add to " + p.name + ": which field?"
	m.picker = newPickerWith(pickerPersonKeys, spec, items)
	return m, nil
}

// choosePersonKey asks the chosen field's value, or a new field's name.
func (m Model) choosePersonKey(value string) (Model, tea.Cmd) {
	m = m.showPerson("") // the card stays behind the field's prompt
	if value == personNewKey {
		return m.openPrompt(promptPersonKey), nil
	}
	m.aimedAt.person.key, m.aimedAt.person.list = value, false
	return m.openPrompt(promptPersonValue), nil
}

// submitPersonKey names a new field and asks its value.
func (m Model) submitPersonKey(input string) (Model, tea.Cmd) {
	key := strings.TrimSpace(input)
	switch {
	case key == "":
		return m.showPerson(""), nil
	case strings.ContainsAny(key, ":#\n") || strings.HasPrefix(key, "-"):
		return m.showPerson("").say("a field's name cannot hold : or #, nor start with -"), nil
	}
	m.aimedAt.person.key, m.aimedAt.person.list = key, false
	return m.showPerson("").openPrompt(promptPersonValue), nil
}

// submitPersonValue writes the field: commas make a list, empty removes it. A person with no
// card gets one first.
func (m Model) submitPersonValue(input string) (Model, tea.Cmd) {
	p := m.aimedAt.person
	var values []string
	for v := range strings.SplitSeq(input, ",") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	if !p.found && len(values) == 0 {
		return m.showPerson(""), nil
	}
	// Several values are a list whatever list says (phonebook.Book.Set).
	book, ids, name, path, key, list := m.book(), m.personIdentifiers(p.id), p.name, p.card.Path, p.key, p.list
	return m.doing("writing " + isolate(name) + "'s " + key + "…"), func() tea.Msg {
		if path == "" {
			card, err := book.Create(name, ids)
			if err != nil {
				return personWrittenMsg{err: err}
			}
			path = card.Path
		}
		card, err := book.Set(path, key, values, list)
		return personWrittenMsg{card: card, err: err}
	}
}

// handlePersonWritten shows the card as written, on the row written.
func (m Model) handlePersonWritten(msg personWrittenMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not write the card", msg.err), nil
	}
	key := m.aimedAt.person.key
	m.aimedAt.person.card, m.aimedAt.person.found = msg.card, true
	m = m.say(isolate(m.aimedAt.person.name) + "'s " + key + " written")
	// What is drawn beside names, and searched, reads the cards held.
	m, cards := m.showPerson(key).loadCards(false, "")
	return m, cards
}

// cancelPerson goes back to the card from a prompt about one of its fields.
func (m Model) cancelPerson() Model { return m.showPerson(m.aimedAt.person.key) }

// personIdentifiers is every ID the phonebook may know id's person by: IDs the directory links
// to theirs and their identity's IDs (domain.CardIdentifiers).
func (m Model) personIdentifiers(id string) phonebook.Identifiers {
	all := m.dir.dir.Identifiers(id)
	if ident, ok := m.prefs.identities[id]; ok {
		for other, o := range m.prefs.identities {
			if o.key == ident.key && !slices.Contains(all, other) {
				all = append(all, other)
			}
		}
	}
	return phonebook.Identifiers(domain.CardIdentifiers(all))
}

// personOfSender opens the selected message's sender's card.
func (m Model) personOfSender() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok || msg.Sender == "" {
		return m, nil
	}
	return m.openPerson(msg.Sender, stripIsolates(m.processedName(msg)))
}

// personOfMember opens the card of the person under the member list's cursor.
func (m Model) personOfMember() (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok {
		return m, nil
	}
	return m.openPerson(item.value, m.memberShown(item.value))
}

// roomPersonMsg is the one person in a room besides you and the bots, if there is one.
type roomPersonMsg struct {
	room    domain.Room
	members []domain.Member
	err     error
	// said opens what they said rather than their card.
	said bool
}

// personOfRoom opens the card of the room's one person, read from its members.
func (m Model) personOfRoom() (Model, tea.Cmd) { return m.oneOfRoom(false) }

// saidOfRoom opens everything the room's one person said.
func (m Model) saidOfRoom() (Model, tea.Cmd) { return m.oneOfRoom(true) }

func (m Model) oneOfRoom(said bool) (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	ctx, backend := m.ctx, m.backend
	return m, func() tea.Msg {
		members, err := backend.Members(ctx, room.ID, peerMembersLimit)
		return roomPersonMsg{room: room, members: members, err: err, said: said}
	}
}

// handleRoomPerson opens the one person's card, or says the room has no one person.
func (m Model) handleRoomPerson(msg roomPersonMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not read the room's members", msg.err), nil
	}
	peer, many := m.onePerson(msg.members)
	if many {
		return m.say(m.roomName(msg.room) + " is not a room with one person in it"), nil
	}
	if peer.UserID == "" {
		return m.say(m.roomName(msg.room) + " has no one in it but you"), nil
	}
	name := stripIsolates(m.processedName(domain.Message{RoomID: msg.room.ID, Sender: peer.UserID, SenderName: peer.DisplayName}))
	if msg.said {
		return m.openSaid(peer.UserID, name)
	}
	return m.openPerson(peer.UserID, name)
}

// personInView is :person: the selected message's sender in the timeline, else the open room's
// one person.
func (m Model) personInView() (Model, tea.Cmd) {
	if m.focus == paneTimeline {
		if _, ok := m.selectedMessage(); ok {
			return m.personOfSender()
		}
	}
	return m.personOfRoom()
}
