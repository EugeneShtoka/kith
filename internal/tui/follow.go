package tui

import (
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Following a Matrix link (gx, where followsHere) moves inside the client: a joined room
// or message goes there; an unjoined room asks before joining; a person opens the
// switcher filtered to them, since following must not create anything.

// jumpTarget is a message to select once the timeline contains it (see
// resolveJump); why names who asked.
type jumpTarget struct {
	to  domain.EventID
	why string
}

// messageHrefs are the targets of named links in a message's formatted body, the only
// place a mention pill's MXID appears.
func (m Model) messageHrefs(msg domain.Message) []string {
	_, spans, ok := m.formattedBody(msg)
	if !ok {
		return nil
	}
	hrefs := make([]string, 0, len(spans))
	for i := range spans {
		if spans[i].Href != "" {
			hrefs = append(hrefs, spans[i].Href)
		}
	}
	return hrefs
}

// followsHere reports whether link opens in kith rather than the browser: a Matrix
// address, or a Telegram or WhatsApp chat link, on a network one of the room list's
// chats is on (a bridged chat is Matrix's, which looks up no Telegram link).
func (m Model) followsHere(link string) bool {
	network, ok := domain.LinkNetwork(link)
	if _, place := domain.ParsePlace(link); place {
		network, ok = domain.ProtocolMatrix, true
	}
	return ok && slices.ContainsFunc(m.rooms.joined, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) == network
	})
}

// placeLabel is what to call a place, and what kind of thing it is.
func (m Model) placeLabel(place domain.Place) (label, detail string) {
	switch place.Kind {
	case domain.PlaceNone:
	case domain.PlacePerson:
		return m.mentionedName(placeUser(place), m.openRoom, localpart(place.User)), "person"
	case domain.PlaceRoom, domain.PlaceEvent:
		kind := "room"
		if place.Kind == domain.PlaceEvent {
			kind = "message"
		}
		if room, found := m.placeRoom(place); found {
			return m.roomName(room), kind
		}
		return place.Room, kind + ", not joined"
	}
	return place.String(), ""
}

// goToPlace moves to a single resolved place.
func (m Model) goToPlace(place domain.Place) (Model, tea.Cmd) {
	switch place.Kind {
	case domain.PlaceNone:
	case domain.PlacePerson:
		return m.goToPerson(placeUser(place))
	case domain.PlaceRoom:
		return m.goToPlaceRoom(place, "")
	case domain.PlaceEvent:
		return m.goToPlaceRoom(place, place.Event)
	}
	return m, nil
}

// goToPerson opens the switcher filtered to somebody's name, or their MXID when they
// have none.
func (m Model) goToPerson(user string) (Model, tea.Cmd) {
	next, cmd := m.openJump()
	name := next.mentionedName(user, next.openRoom)
	if name == "" {
		name = user
	}
	next.picker.filter = name
	next.picker = next.picker.refilter()
	next = next.say("rooms with " + isolate(name))
	return next, cmd
}

// goToPlaceRoom goes to a room, and to a message inside it when the link named one:
// as the account already in it sees it, picked when several are (pickAccount).
func (m Model) goToPlaceRoom(place domain.Place, event domain.EventID) (Model, tea.Cmd) {
	rooms := m.placeRooms(place)
	if len(rooms) == 0 {
		// An alias or an unjoined room: ask before joining.
		m.confirm = confirmState{action: pendingJoinPlace, address: place.Room, via: place.Via, event: event}
		return m, nil
	}
	if len(rooms) == 1 {
		return m.openPlaceRoom(rooms[0], event)
	}
	return m.pickAccount(accountThen{kind: thenOpenPlace, network: domain.ProtocolMatrix, rooms: rooms, event: event})
}

// openPlaceRoom goes to room, and to event in it when there is one.
func (m Model) openPlaceRoom(room domain.Room, event domain.EventID) (Model, tea.Cmd) {
	if event != "" {
		// A Matrix link names the event as Matrix writes it; the room says whose view it is.
		m.jump.to, m.jump.why = domain.TypedEventID(room.ID, string(event)), "linked message"
	}
	next, cmd := m.goToRoom(room.ID)
	next.focus = paneTimeline
	return next, cmd
}

// placeRoom is the first joined room a place names (placeRooms).
func (m Model) placeRoom(place domain.Place) (domain.Room, bool) {
	if rooms := m.placeRooms(place); len(rooms) > 0 {
		return rooms[0], true
	}
	return domain.Room{}, false
}

// placeRooms is the joined room a place names: by its kith ID, or for a Matrix link by
// its Matrix room ID, as each of our Matrix accounts in it sees it. An alias names no
// cached room.
func (m Model) placeRooms(place domain.Place) []domain.Room {
	if room, found := m.roomByID(domain.RoomID(place.Room)); found {
		return []domain.Room{room}
	}
	return domain.MatrixPlaceRooms(place, m.rooms.all)
}

// placeUser is the person a place names, in kith's form.
func placeUser(place domain.Place) string { return domain.TypedUserID(place.User) }

// joinPlace joins the linked room after confirmation, on the account picked
// (pickAccount). The linked event is not jumped to: history arrives over sync only
// after the join.
func (m Model) joinPlace(pending confirmState) (Model, tea.Cmd) {
	return m.pickAccount(accountThen{kind: thenJoin, network: domain.ProtocolMatrix, address: pending.address, via: pending.via})
}

// WithFollow gives the model a link to follow as soon as it knows where it is.
func (m Model) WithFollow(uri string) Model {
	m.followAt = uri
	return m
}

// followPendingLink follows the startup link once the room list has arrived; false
// until then, so it is safe to call on every refresh.
func (m Model) followPendingLink() (Model, tea.Cmd, bool) {
	if m.followAt == "" || len(m.rooms.joined) == 0 {
		return m, nil, false
	}
	uri := m.followAt
	m.followAt = ""
	next, cmd := m.followLink(uri)
	return next, cmd, true
}

// followLink goes where uri points and says so after the move, since opening a room
// sets its own status and the last writer wins. Matrix is tried first; a tg:// link
// looks up a chat by its stored @username, a WhatsApp link by its number, else the
// network is asked; a Signal link is always asked about, since no chat stores one.
func (m Model) followLink(uri string) (Model, tea.Cmd) {
	if place, ok := domain.ParsePlace(uri); ok {
		label, _ := m.placeLabel(place)
		next, cmd := m.goToPlace(place)
		return next.say("followed a link to " + isolate(label)), cmd
	}
	if link, ok := domain.ParseTelegramLink(uri); ok {
		return m.followTelegramLink(link, uri)
	}
	if link, ok := domain.ParseWhatsAppLink(uri); ok {
		return m.followWhatsAppLink(link, uri)
	}
	if _, ok := domain.ParseSignalLink(uri); ok {
		return m.pickAccount(accountThen{kind: thenResolve, network: domain.ProtocolSignal, uri: uri})
	}
	return m.say("not a Matrix, Telegram, WhatsApp or Signal link: " + uri), nil
}

// followTelegramLink goes to the cached Telegram chat whose @username (or private
// channel ID) matches link, else to the one Telegram finds (handleLinkResolved).
func (m Model) followTelegramLink(link domain.TelegramLink, uri string) (Model, tea.Cmd) {
	return m.followLinkTo(domain.ProtocolTelegram, m.rooms.telegramLink(link), uri, link.Post, link.Text)
}

// followWhatsAppLink goes to the cached WhatsApp chat with link's number, else to the
// one WhatsApp finds (handleLinkResolved).
func (m Model) followWhatsAppLink(link domain.WhatsAppLink, uri string) (Model, tea.Cmd) {
	return m.followLinkTo(domain.ProtocolWhatsApp, m.rooms.whatsAppLink(link), uri, 0, link.Text)
}

// followLinkTo opens the chat a link names as the account picked among those in it
// (rooms), else looks it up on the account picked among network's (pickAccount).
func (m Model) followLinkTo(network domain.Protocol, rooms []domain.Room, uri string, post int, text string) (Model, tea.Cmd) {
	if len(rooms) == 0 {
		return m.pickAccount(accountThen{kind: thenResolve, network: network, uri: uri, post: post, text: text})
	}
	return m.pickAccount(accountThen{kind: thenOpenLinked, network: network, rooms: rooms, post: post, text: text})
}

// openLinked opens a chat a link named, at the post it named, with the message it
// suggests in the composer.
func (m Model) openLinked(room domain.Room, post int, text string) (Model, tea.Cmd) {
	label := m.roomName(room)
	place := domain.Place{Kind: domain.PlaceRoom, Room: string(room.ID)}
	if post > 0 {
		place.Kind = domain.PlaceEvent
		place.Event = domain.EventID(string(room.ID) + "/" + strconv.Itoa(post))
	}
	next, cmd := m.goToPlace(place)
	return next.suggested(text).say("followed a link to " + isolate(label)), cmd
}

// suggested puts a link's suggested message in the composer, only when it holds no
// draft of its own: the link offers words, it does not take the user's.
func (m Model) suggested(text string) Model {
	if text == "" || m.compose.input != "" || m.compose.isEditing() {
		return m
	}
	m = m.store(fieldComposer, newEditor(text).end())
	return m.trackComposerDraft()
}

// linkResolvedMsg is the chat a link names, as its network found it on the account on;
// post and text are the link's own.
type linkResolvedMsg struct {
	uri    string
	on     domain.RoomOwner
	post   int
	text   string
	target domain.LinkTarget
	err    error
}

// resolveLinkCmd asks the account on for the chat a link names.
func (m Model) resolveLinkCmd(uri string, on domain.RoomOwner, post int, text string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		target, err := backend.ResolveLink(ctx, uri, on)
		return linkResolvedMsg{uri: uri, on: on, post: post, text: text, target: target, err: err}
	}
}

// handleLinkResolved opens the chat a network found for a link: one to join is asked
// about first; one no listing holds yet (somebody to write to for the first time) is
// opened as a fresh chat, made by its first message.
func (m Model) handleLinkResolved(msg linkResolvedMsg) (Model, tea.Cmd) {
	t := msg.target
	switch {
	case msg.err != nil:
		return m.sayErr("no chat found for "+msg.uri, msg.err), nil
	case t.Join:
		m.confirm = confirmState{action: pendingJoinLink, address: msg.uri, on: msg.on, room: t.Room, body: t.About}
		return m.clearStatus(), nil
	case t.Room.ID == "":
		return m.say("no chat found for " + msg.uri), nil
	}
	m.rooms = m.rooms.withFresh(t.Room)
	m = m.refreshPlaces().rebuiltRail()
	room, _ := m.rooms.byID(t.Room.ID)
	return m.openLinked(room, msg.post, msg.text)
}

// joinLinkPrompt is the question before joining the chat a link names.
func joinLinkPrompt(pending confirmState) string {
	name := pending.room.Name
	if name == "" {
		name = pending.address
	}
	if pending.body != "" {
		return "join " + isolate(name) + " (" + pending.body + ")? you are not in it"
	}
	return "join " + isolate(name) + "? you are not in it"
}

// joinLink joins the chat a link names after confirmation, and opens it once listed.
func (m Model) joinLink(pending confirmState) (Model, tea.Cmd) {
	name := pending.room.Name
	if name == "" {
		name = pending.address
	}
	m = m.doing("joining " + isolate(name) + "…")
	ctx, backend, uri, on := m.ctx, m.backend, pending.address, pending.on
	return m, func() tea.Msg {
		roomID, err := backend.JoinLink(ctx, uri, on)
		return joinedMsg{roomID: roomID, err: err, enter: true}
	}
}

// followSource is a backend that hands over links clicked elsewhere on the desktop
// (`kith --open`). Optional: it is not Matrix, so api.Backend does not carry it.
type followSource interface {
	Follows() <-chan string
}

// listenFollowCmd waits for a handed-over link; nil for backends without the stream.
func (m Model) listenFollowCmd() tea.Cmd {
	source, ok := m.backend.(followSource)
	if !ok {
		return nil
	}
	return listen(m.ctx, source.Follows(),
		func(uri string) tea.Msg { return followMsg{uri: uri} })
}

// changesSource is a backend that says what changed in the daemon (a network's rooms,
// an account's state). Optional, like followSource.
type changesSource interface {
	Changed() <-chan domain.Change
}

// listenChangesCmd waits for something to change in the daemon; nil for backends
// without the stream.
func (m Model) listenChangesCmd() tea.Cmd {
	source, ok := m.backend.(changesSource)
	if !ok {
		return nil
	}
	return listen(m.ctx, source.Changed(), func(c domain.Change) tea.Msg { return changedMsg{change: c} })
}

// changedMsg is something having changed in the daemon.
type changedMsg struct{ change domain.Change }

// handleChanged reads again what changed, and listens for the next change.
func (m Model) handleChanged(msg changedMsg) (Model, tea.Cmd) {
	switch msg.change {
	case domain.ChangeRooms:
		return m, tea.Batch(m.loadRoomsCmd(), m.listenChangesCmd())
	case domain.ChangeAccounts:
		return m, tea.Batch(m.loadAccountStatesCmd(), m.listenChangesCmd())
	case domain.ChangeMessages:
		// The message stream could not resume: what arrived meanwhile is read from the
		// cache instead.
		cmds := []tea.Cmd{m.lastMessagesCmd(), m.listenChangesCmd()}
		if m.openRoom != "" {
			cmds = append(cmds, m.loadRoomCmd(m.openRoom))
		}
		return m, tea.Batch(cmds...)
	}
	return m, m.listenChangesCmd()
}

// followMsg is a link the desktop handed to this client.
type followMsg struct{ uri string }

// handleLinking is news of a link: one handed over, the chat its network found, or the
// accounts one is picked among (accountpick.go).
func (m Model) handleLinking(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case linkResolvedMsg:
		return m.handleLinkResolved(msg)
	case accountsPickedMsg:
		return m.handleAccountsPicked(msg)
	case accountListMsg:
		return m.handleAccountList(msg)
	}
	handed, _ := msg.(followMsg)
	return m.handleFollow(handed)
}

// handleFollow goes where a handed-over link points, brings this window forward
// ([clipboard] raise_command), and re-arms the listener. A startup link does not
// raise: its terminal was just opened.
func (m Model) handleFollow(msg followMsg) (Model, tea.Cmd) {
	next, cmd := m.followLink(msg.uri)
	return next, tea.Batch(cmd, next.listenFollowCmd(), next.raiseWindowCmd())
}
