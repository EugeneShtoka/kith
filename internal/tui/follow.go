package tui

import (
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Following a Matrix link moves inside the client (gx opens a browser): a joined room
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

// placeItems turns places into picker rows, labeled by their local names.
func placeItems(m Model, places []domain.Place) []pickerItem {
	items := make([]pickerItem, 0, len(places))
	for _, place := range places {
		label, detail := m.placeLabel(place)
		items = append(items, pickerItem{
			label:  isolate(label),
			detail: detail,
			// URI, not String: String drops the routing servers.
			value: place.URI(),
			match: label + " " + place.String(),
		})
	}
	return items
}

// placeLabel is what to call a place, and what kind of thing it is.
func (m Model) placeLabel(place domain.Place) (label, detail string) {
	switch place.Kind {
	case domain.PlaceNone:
	case domain.PlacePerson:
		return m.mentionedName(place.User, m.openRoom, localpart(place.User)), "person"
	case domain.PlaceRoom, domain.PlaceEvent:
		kind := "room"
		if place.Kind == domain.PlaceEvent {
			kind = "message"
		}
		if room, found := m.roomByID(domain.RoomID(place.Room)); found {
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
		return m.goToPerson(place.User)
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

// goToPlaceRoom goes to a room, and to a message inside it when the link named one.
func (m Model) goToPlaceRoom(place domain.Place, event domain.EventID) (Model, tea.Cmd) {
	room, found := m.roomByID(domain.RoomID(place.Room))
	if !found {
		// An alias or an unjoined room: ask before joining.
		m.confirm = confirmState{action: pendingJoinPlace, address: place.Room, via: place.Via, event: event}
		return m, nil
	}
	if event != "" {
		m.jump.to, m.jump.why = event, "linked message"
	}
	next, cmd := m.goToRoom(room.ID)
	next.focus = paneTimeline
	return next, cmd
}

// joinPlace joins the linked room after confirmation. The linked event is not jumped
// to: history arrives over sync only after the join.
func (m Model) joinPlace(pending confirmState) (Model, tea.Cmd) {
	m = m.say("joining " + pending.address + "…")
	return m, m.joinRoomCmd(pending.address, pending.via...)
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
// network is asked.
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
	return m.say("not a Matrix, Telegram or WhatsApp link: " + uri), nil
}

// followTelegramLink goes to the cached Telegram chat whose @username (or private
// channel ID) matches link, else to the one Telegram finds (handleLinkResolved).
func (m Model) followTelegramLink(link domain.TelegramLink, uri string) (Model, tea.Cmd) {
	room, found := m.rooms.telegramLink(link)
	if !found {
		return m.doing("looking up " + uri + "…"), m.resolveLinkCmd(uri, link.Post, link.Text)
	}
	return m.openLinked(room, link.Post, link.Text)
}

// followWhatsAppLink goes to the cached WhatsApp chat with link's number, else to the
// one WhatsApp finds (handleLinkResolved).
func (m Model) followWhatsAppLink(link domain.WhatsAppLink, uri string) (Model, tea.Cmd) {
	room, found := m.rooms.whatsAppLink(link)
	if !found {
		return m.doing("looking up " + uri + "…"), m.resolveLinkCmd(uri, 0, link.Text)
	}
	return m.openLinked(room, 0, link.Text)
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

// linkResolvedMsg is the chat a link names, as its network found it; post and text are
// the link's own.
type linkResolvedMsg struct {
	uri    string
	post   int
	text   string
	target domain.LinkTarget
	err    error
}

// resolveLinkCmd asks the link's network for the chat it names.
func (m Model) resolveLinkCmd(uri string, post int, text string) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		target, err := backend.ResolveLink(ctx, uri)
		return linkResolvedMsg{uri: uri, post: post, text: text, target: target, err: err}
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
		m.confirm = confirmState{action: pendingJoinLink, address: msg.uri, room: t.Room, body: t.About}
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
	ctx, backend, uri := m.ctx, m.backend, pending.address
	return m, func() tea.Msg {
		roomID, err := backend.JoinLink(ctx, uri)
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

// roomsSource is a backend that says when a network rewrote its rooms. Optional, like
// followSource.
type roomsSource interface {
	RoomsChanged() <-chan struct{}
}

// listenRoomsCmd waits for a network to rewrite its rooms; nil for backends without
// the stream.
func (m Model) listenRoomsCmd() tea.Cmd {
	source, ok := m.backend.(roomsSource)
	if !ok {
		return nil
	}
	return listen(m.ctx, source.RoomsChanged(), func(struct{}) tea.Msg { return roomsChangedMsg{} })
}

// roomsChangedMsg is a network having rewritten its rooms.
type roomsChangedMsg struct{}

// followMsg is a link the desktop handed to this client.
type followMsg struct{ uri string }

// handleLinking is news of a link: one handed over, or the chat its network found.
func (m Model) handleLinking(msg tea.Msg) (Model, tea.Cmd) {
	if resolved, ok := msg.(linkResolvedMsg); ok {
		return m.handleLinkResolved(resolved)
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
