package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Following a Matrix link moves you inside the client (gx would open a browser): a
// joined room or message → go there; an unjoined room → ask before joining; a person →
// the switcher filtered to them, since following a link must not create anything.

// jumpTarget is a message to select once the timeline contains it (see
// resolveJump); why names who asked.
type jumpTarget struct {
	to  domain.EventID
	why string
}

// messageHrefs are the targets of the named links in a message's formatted body — the
// only place a mention pill's MXID appears.
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

// goToPlace is the move itself, once there is one place to go to.
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

// goToPerson opens the switcher filtered to somebody's name, or their MXID when there
// is no name for them.
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

// joinPlace joins the room a link pointed at, after the question. The linked event is
// not jumped to: history arrives over sync only after the join.
func (m Model) joinPlace(pending confirmState) (Model, tea.Cmd) {
	m = m.say("joining " + pending.address + "…")
	return m, m.joinRoomCmd(pending.address, pending.via...)
}

// WithFollow gives the model a link to follow as soon as it knows where it is.
func (m Model) WithFollow(uri string) Model {
	m.followAt = uri
	return m
}

// followPendingLink follows the startup link once the room list has arrived; it
// answers false until then, so it is safe to call on every refresh.
func (m Model) followPendingLink() (Model, tea.Cmd, bool) {
	if m.followAt == "" || len(m.rooms.joined) == 0 {
		return m, nil, false
	}
	uri := m.followAt
	m.followAt = ""
	next, cmd := m.followLink(uri)
	return next, cmd, true
}

// followLink goes where uri points and says so — after the move, since opening a room
// sets its own status and the last writer wins.
func (m Model) followLink(uri string) (Model, tea.Cmd) {
	place, ok := domain.ParsePlace(uri)
	if !ok {
		return m.say("not a Matrix link: " + uri), nil
	}
	label, _ := m.placeLabel(place)
	next, cmd := m.goToPlace(place)
	return next.say("followed a link to " + isolate(label)), cmd
}

// followSource is a backend that can hand over links clicked elsewhere on the desktop
// (`kith --open`). Optional, because it is not Matrix and api.Backend should not
// carry it.
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

// roomsSource is a backend that says when a network rewrote its rooms. Optional, as
// followSource is.
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

// handleFollow goes where a handed-over link points, and re-arms the listener.
func (m Model) handleFollow(msg followMsg) (Model, tea.Cmd) {
	next, cmd := m.followLink(msg.uri)
	return next, tea.Batch(cmd, next.listenFollowCmd())
}
