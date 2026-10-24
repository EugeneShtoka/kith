package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// publicRoomsLoadedMsg carries one page of the Matrix public rooms directory for
// the chooser; err !=nil says the request failed and nothing is to show.
type publicRoomsLoadedMsg struct {
	on   domain.RoomOwner
	page domain.PublicRoomsPage
	err  error
}

// publicRoomJoinedMsg is the outcome of chooseJoinPublicRoom's JoinRoom call.
type publicRoomJoinedMsg struct {
	roomID domain.RoomID
	name   string
	err    error
}

// openPublicRooms is `/discover`: fetch the first page of the directory the account
// picked sees (pickAccount), and open the chooser.
func (m Model) openPublicRooms() (Model, tea.Cmd) {
	return m.pickAccount(accountThen{kind: thenDiscover, network: domain.ProtocolMatrix})
}

// publicRoomsCmd fetches one page, as the account on sees it; since is empty for the
// first page.
func (m Model) publicRoomsCmd(on domain.RoomOwner, since string) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		page, err := backend.PublicRooms(ctx, on, "", since)
		return publicRoomsLoadedMsg{on: on, page: page, err: err}
	}
}

// handlePublicRoomsLoaded fills the chooser with the fetched rooms, or says why
// the fetch failed.
func (m Model) handlePublicRoomsLoaded(msg publicRoomsLoadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("list public rooms", msg.err), nil
	}
	if len(msg.page.Rooms) == 0 {
		return m.say("the directory is empty"), nil
	}
	items := make([]pickerItem, 0, len(msg.page.Rooms))
	for _, r := range msg.page.Rooms {
		items = append(items, publicRoomItem(msg.on, r))
	}
	m.picker = newPicker(pickerPublicRooms, items)
	return m, nil
}

// publicRoomItem renders one directory row of the account on: the room's name (or
// alias, or ID), its member count and a short topic in detail.
func publicRoomItem(on domain.RoomOwner, r domain.PublicRoom) pickerItem {
	label := r.Name
	if label == "" {
		label = r.Alias
	}
	if label == "" {
		label = string(r.ID)
	}
	detail := fmt.Sprintf("%d members", r.Members)
	if topic := strings.TrimSpace(r.Topic); topic != "" {
		detail += " · " + flatten(topic)
	}
	// value is the alias when known (joining by alias keeps the server hint for
	// routing), on the directory's account; otherwise the room ID, which names it.
	target := string(on) + r.Alias
	if r.Alias == "" {
		target = string(r.ID)
	}
	return pickerItem{
		label:  label,
		detail: detail,
		value:  target,
		match:  label + " " + detail + " " + string(r.ID) + " " + r.Alias,
	}
}

// chooseJoinPublicRoom joins the room the picker's cursor is on.
func (m Model) chooseJoinPublicRoom(item pickerItem) (Model, tea.Cmd) {
	target := item.value
	label := item.label
	backend, ctx := m.backend, m.ctx
	return m.closePicker(), func() tea.Msg {
		roomID, err := backend.JoinRoom(ctx, target, nil)
		return publicRoomJoinedMsg{roomID: roomID, name: label, err: err}
	}
}

// handlePublicRoomJoined says the outcome of the join; the room becomes an
// ordinary joined room on the next sync and goes to the rail list.
func (m Model) handlePublicRoomJoined(msg publicRoomJoinedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("join "+msg.name, msg.err), nil
	}
	return m.say("joined " + msg.name), nil
}
