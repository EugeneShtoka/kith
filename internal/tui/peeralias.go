package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A DM named after its one peer lends that peer the room's local name; an identity alias wins.

// peerMembersLimit bounds members read per named room; a room this big is not a DM.
const peerMembersLimit = 8

type peerState struct {
	// key is the peerKey the members were last asked for.
	key     string
	members map[domain.RoomID][]domain.Member
	// aliases maps a peer's user ID to its room's name.
	aliases map[string]string
	// rev moves when aliases change, for the derived cache.
	rev uint64
}

// peerMembersMsg is the named rooms' members, read for key.
type peerMembersMsg struct {
	key     string
	members map[domain.RoomID][]domain.Member
}

// namedRooms is the known rooms with a [[display.name]].
func (m Model) namedRooms() []domain.Room {
	ids := slices.Sorted(maps.Keys(m.prefs.roomAliases))
	rooms := make([]domain.Room, 0, len(ids))
	for _, id := range ids {
		if room, ok := m.roomByID(id); ok && !room.IsInvite() {
			rooms = append(rooms, room)
		}
	}
	return rooms
}

// peerKey changes when the named rooms or their names change.
func peerKey(rooms []domain.Room) string {
	var b strings.Builder
	for i := range rooms {
		b.WriteString(string(rooms[i].ID) + "\x00" + rooms[i].Name + "\x00")
	}
	return b.String()
}

// refreshPeers reads the named rooms' members when peerKey changed.
func (m Model) refreshPeers() (Model, tea.Cmd) {
	rooms := m.namedRooms()
	key := peerKey(rooms)
	if key == m.peers.key {
		return m.withPeerAliases(), nil
	}
	m.peers.key = key
	if len(rooms) == 0 {
		m.peers.members = nil
		return m.withPeerAliases(), nil
	}
	ctx, backend, log := m.ctx, m.backend, m.log
	return m.withPeerAliases(), func() tea.Msg {
		out := make(map[domain.RoomID][]domain.Member, len(rooms))
		for i := range rooms {
			id := rooms[i].ID
			list, err := backend.Members(ctx, id, peerMembersLimit)
			if err != nil {
				// An unread room names nobody; the next change retries it.
				if log != nil {
					log.Warn("read a named room's members", "room", id, "err", err)
				}
				continue
			}
			out[id] = list
		}
		return peerMembersMsg{key: key, members: out}
	}
}

// handlePeerMembers keeps the answer for the current key; a stale one is dropped.
func (m Model) handlePeerMembers(msg peerMembersMsg) (Model, tea.Cmd) {
	if msg.key != m.peers.key {
		return m, nil
	}
	m.peers.members = msg.members
	return m.withPeerAliases(), nil
}

// withPeerAliases names each named room's peer. isMe and room aliases change apart from the
// members, so it runs after each.
func (m Model) withPeerAliases() Model {
	out := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(m.peers.members)) {
		alias := m.prefs.roomAliases[id]
		room, ok := m.roomByID(id)
		if alias == "" || !ok {
			continue
		}
		if peer, ok := m.peerOf(room, m.peers.members[id]); ok {
			if _, taken := out[peer]; !taken {
				out[peer] = alias
			}
		}
	}
	if !maps.Equal(out, m.peers.aliases) {
		m.peers.aliases = out
		m.peers.rev++
	}
	return m
}

// peerOf is the room's one member who is neither me nor a bridge bot, when the room's own name
// is that member's name.
func (m Model) peerOf(room domain.Room, members []domain.Member) (string, bool) {
	if len(members) >= peerMembersLimit || room.Name == "" {
		return "", false
	}
	var peer domain.Member
	for _, member := range members {
		if m.isMe(member.UserID) || domain.IsBridgeBot(member.UserID) {
			continue
		}
		if peer.UserID != "" {
			return "", false
		}
		peer = member
	}
	if peer.UserID == "" || peer.DisplayName != room.Name {
		return "", false
	}
	return peer.UserID, true
}
