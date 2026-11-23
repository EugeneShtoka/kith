package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
)

// linkState is the daemon streams' state; it only drives the badge.
type linkState struct {
	// detached is set while the streams are down.
	detached bool
	// ended: the streams are closed for good (Remote.Start returned); the badge asks for a restart.
	ended bool
	// restart restarts the daemon and returns once it answers (RunOptions.RestartDaemon).
	restart func(context.Context) error
	// heldSince is set while kith-proxy answers from its store, the bridge out of reach:
	// when it last heard the bridge (api.Held).
	heldSince time.Time
}

// heldMsg is whether the answers are held, and since when (zero: they are not).
type heldMsg struct{ since time.Time }

// loadHeldCmd asks whether the answers come from kith-proxy's store; nil for a
// backend that cannot say. A failure is silent: the link badge says the daemon is lost.
func (m Model) loadHeldCmd() tea.Cmd {
	src, ok := m.backend.(api.Held)
	if !ok {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		since, err := src.HeldSince(ctx)
		if err != nil {
			return nil
		}
		return heldMsg{since: since}
	}
}

// handleAttached shows the daemon connection state and re-reads, on reconnect, what
// no stream replays. Messages are replayed: the message stream resumes after the last
// one this client got, or says to read them again (ChangeMessages).
func (m Model) handleAttached(msg attachedMsg) (Model, tea.Cmd) {
	m.link.detached = !msg.attached
	if !msg.attached {
		return m.say("lost the daemon — reconnecting"), m.listenAttachedCmd()
	}
	m = m.say("daemon back — catching up")
	// kith-proxy switching between the bridge and its store ends the streams, so each
	// reattachment may change which answers.
	return m, tea.Batch(m.listenAttachedCmd(), m.loadHeldCmd(), m.loadRoomsCmd(), m.loadUnreadCmd(),
		m.loadInvitesCmd(), m.lastMessagesCmd(), m.readConfigCmd(false), m.loadRailCmd(), m.loadDraftsCmd())
}

// connectionBadge is the right-edge marker while the daemon is lost; it outranks the
// silence badge.
func (m Model) connectionBadge() string {
	if !m.link.detached {
		if !m.link.heldSince.IsZero() {
			return m.theme.Badge(true).Render(" offline · as of " + m.link.heldSince.Format("Jan 2 15:04") + " ")
		}
		return ""
	}
	if m.link.ended {
		// Start returned: the streams are closed for good in this process.
		return m.theme.Badge(true).Render(" streams stopped · restart kith ")
	}
	return m.theme.Badge(true).Render(" daemon lost · reconnecting ")
}
