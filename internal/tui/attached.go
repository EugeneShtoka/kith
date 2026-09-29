package tui

import (
	tea "charm.land/bubbletea/v2"
)

// linkState is the daemon streams' state; it only drives the badge.
type linkState struct {
	// detached is set while the streams are down.
	detached bool
	// ended says the streams are closed for good (Remote.Start returned); the badge asks for a restart.
	ended bool
	// seatLost: another window took the daemon's seat, and this one is quitting.
	seatLost bool
}

// handleAttached shows the daemon connection state and, on reconnect, re-reads the
// open room: the daemon kept syncing while we were away, but no stream will replay
// what arrived in the meantime.
func (m Model) handleAttached(msg attachedMsg) (Model, tea.Cmd) {
	m.link.detached = !msg.attached
	if !msg.attached {
		return m.say("lost the daemon — reconnecting"), m.listenAttachedCmd()
	}
	m = m.say("daemon back — catching up")
	cmds := []tea.Cmd{m.listenAttachedCmd(), m.loadRoomsCmd(), m.loadUnreadCmd(),
		m.loadInvitesCmd(), m.lastMessagesCmd()}
	if m.openRoom != "" {
		cmds = append(cmds, m.loadRoomCmd(m.openRoom))
	}
	return m, tea.Batch(cmds...)
}

// connectionBadge is the right-edge marker while the daemon is lost; it outranks the
// silence badge.
func (m Model) connectionBadge() string {
	if !m.link.detached {
		return ""
	}
	if m.link.ended {
		// Start returned: the streams are closed for good in this process.
		return m.theme.Badge(true).Render(" streams stopped · restart kith ")
	}
	return m.theme.Badge(true).Render(" daemon lost · reconnecting ")
}
