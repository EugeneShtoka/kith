package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// selvesMsg is every ID that is this person, per the daemon (api.Identity).
type selvesMsg struct {
	ids []string
	err error
}

// loadSelvesCmd asks who this person is: at start, and when rooms show a new account
// (see selvesAfterRooms).
func (m Model) loadSelvesCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Selves, func(ids []string, err error) tea.Msg { return selvesMsg{ids: ids, err: err} })
}

// handleSelves keeps the IDs; a failed ask keeps the last ones.
func (m Model) handleSelves(msg selvesMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		m.selves = msg.ids
	}
	return m.withPeerAliases(), nil
}

// isMe reports whether id is this person on any network (own accounts and bridge puppets).
func (m Model) isMe(id string) bool {
	return id != "" && slices.Contains(m.selves, id)
}

func (m Model) selfIDs() []string {
	ids := make([]string, 0, len(m.selves))
	for _, id := range m.selves {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// selfIn is this person's first ID on room's network, or "".
func (m Model) selfIn(room domain.RoomID) string {
	network := domain.NetworkOf(string(room))
	for _, id := range m.selfIDs() {
		if domain.NetworkOf(id) == network {
			return id
		}
	}
	return ""
}

// selvesAfterRooms asks again who this person is when rooms arrive from a new
// account: a login (a phone just linked) adds IDs, and its rooms are the first sign.
// The startup ask covers the first list.
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
	return m, tea.Batch(m.loadSelvesCmd(), m.loadAccountsCmd())
}
