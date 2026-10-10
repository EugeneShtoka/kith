package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A bridge names a private chat after the person in it, so naming the room names them
// too: a room with a [[display.name]] whose own name is its one other member's name
// gives that member the room's name, wherever they are shown. An identity's alias
// still comes first. Only the named rooms' members are read.

// peerMembersLimit bounds the members read per named room: a room with this many is
// not a private chat.
const peerMembersLimit = 8

// peerState is the named rooms' members and the names they give their peers.
type peerState struct {
	// key is the named rooms the members were last asked for (peerKey).
	key string
	// members is each named room's members, as last read.
	members map[domain.RoomID][]domain.Member
	// aliases is each peer's room name, by user ID (withPeerAliases).
	aliases map[string]string
	// rev moves when aliases changes, for the timeline's derived cache.
	rev uint64
}

// peerMembersMsg is the named rooms' members, read for key.
type peerMembersMsg struct {
	key     string
	members map[domain.RoomID][]domain.Member
}

// namedRooms is each room the list knows that has a [[display.name]], by ID.
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

// peerKey is what the named rooms' peers depend on besides their members: which rooms,
// and their own names, which a bridge changes with the person's.
func peerKey(rooms []domain.Room) string {
	var b strings.Builder
	for i := range rooms {
		b.WriteString(string(rooms[i].ID) + "\x00" + rooms[i].Name + "\x00")
	}
	return b.String()
}

// refreshPeers reads the named rooms' members when the named rooms changed since they
// were last read.
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
				// A room not read names nobody; the next change reads it again.
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

// handlePeerMembers keeps the members read for the current named rooms; an answer for
// an older set is dropped, the newer one being on its way.
func (m Model) handlePeerMembers(msg peerMembersMsg) (Model, tea.Cmd) {
	if msg.key != m.peers.key {
		return m, nil
	}
	m.peers.members = msg.members
	return m.withPeerAliases(), nil
}

// withPeerAliases names each named room's peer by the room's name. Who is this person
// (isMe) and the rooms' aliases change apart from the members, so it runs after each.
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

// peerOf is the one member of room who is neither this person nor a bridge's bot,
// when the room's own name is that member's name.
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
