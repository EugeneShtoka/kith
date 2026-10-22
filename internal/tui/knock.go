package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// knockedMsg is the outcome of a knock. On success it names the room ID the
// homeserver recorded the knock against; the room becomes a joined one only
// when a moderator accepts (an ordinary sync event).
type knockedMsg struct {
	roomID domain.RoomID
	target string // the alias or ID the user typed
	err    error
}

// runKnock is `/knock <room> [reason...]`: records a knock on a Matrix room so
// a moderator can let the user in.
func runKnock(m Model, arg string, _ domain.Room) (Model, tea.Cmd) {
	target, reason, _ := strings.Cut(strings.TrimSpace(arg), " ")
	target = strings.TrimSpace(target)
	reason = strings.TrimSpace(reason)
	if target == "" {
		return m.say("knock on which room? give its ID or alias"), nil
	}
	m.compose.input, m.compose.drafted = "", nil
	return m, m.knockCmd(target, reason)
}

// knockCmd asks the daemon to knock, off the event loop.
func (m Model) knockCmd(target, reason string) tea.Cmd {
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		roomID, err := backend.KnockRoom(ctx, target, reason, nil)
		return knockedMsg{roomID: roomID, target: target, err: err}
	}
}

// handleKnocked reports the server's answer: a success says the knock is in,
// waiting; a failure says what the server refused and leaves the user able to
// retry (perhaps as a plain join).
func (m Model) handleKnocked(msg knockedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("knock on "+msg.target, msg.err), nil
	}
	return m.say("knocked on " + msg.target + " — waiting for a moderator"), nil
}
