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

// isMe reports whether id is this person: the Matrix account kith runs as, or any ID
// the daemon says is theirs (their identities, their WhatsApp accounts).
func (m Model) isMe(id string) bool {
	return id != "" && (id == m.me || slices.Contains(m.selves, id))
}

// selfIDs is every ID isMe accepts, the Matrix account first.
func (m Model) selfIDs() []string {
	ids := make([]string, 0, len(m.selves)+1)
	if m.me != "" {
		ids = append(ids, m.me)
	}
	for _, id := range m.selves {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
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
