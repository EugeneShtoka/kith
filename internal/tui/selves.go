package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// selvesMsg is every ID that is this person, as the daemon knows them (api.Identity).
type selvesMsg struct {
	ids []string
	err error
}

// loadSelvesCmd asks who this person is: at start, and when the room list brings an
// account not seen before (see selvesAfterRooms).
func (m Model) loadSelvesCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Selves, func(ids []string, err error) tea.Msg { return selvesMsg{ids: ids, err: err} })
}

// handleSelves keeps the IDs; a failed ask keeps the last ones.
func (m Model) handleSelves(msg selvesMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		m.selves = msg.ids
	}
	return m, nil
}

// isMe reports whether id is this person: any ID the daemon says is theirs, on any
// network (each network's own accounts, the IDs a bridge posts as for them).
func (m Model) isMe(id string) bool {
	return id != "" && slices.Contains(m.selves, id)
}

// selfIDs is every ID isMe accepts, in the daemon's order.
func (m Model) selfIDs() []string {
	ids := make([]string, 0, len(m.selves))
	for _, id := range m.selves {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// selfIn is this person's ID on room's network, the first the daemon lists when they
// have several there; "" when none.
func (m Model) selfIn(room domain.RoomID) string {
	network := domain.NetworkOf(string(room))
	for _, id := range m.selfIDs() {
		if domain.NetworkOf(id) == network {
			return id
		}
	}
	return ""
}

// selvesAfterRooms asks again who this person is when rooms arrive from a network
// account the list had none from: an account logging in (a WhatsApp phone just
// linked) is what adds IDs, and its rooms are the first sign of it. The first list's
// accounts are what the ask at startup covers.
func (m Model) selvesAfterRooms(rooms []domain.Room) (Model, tea.Cmd) {
	first := m.roomAccounts == nil
	if first {
		m.roomAccounts = []string{}
	}
	fresh := false
	for i := range rooms {
		id := domain.ParseID(string(rooms[i].ID))
		owner := string(id.Network) + "/" + id.Account
		if !slices.Contains(m.roomAccounts, owner) {
			m.roomAccounts = append(m.roomAccounts, owner)
			fresh = true
		}
	}
	if first || !fresh {
		return m, nil
	}
	return m, m.loadSelvesCmd()
}
