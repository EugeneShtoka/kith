package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving an account's own space from the rail signs it out: the network session ends
// and the kept session is deleted; the account stays configured (:login signs back in).
// First asked whether its rooms and history go too, then whether to sign out; nothing
// happens before that last yes.

func (m Model) askSignOut(space domain.Space) Model {
	m.confirm = confirmState{action: pendingSignOutForget, leaving: spaceLeave{space: space}}
	return m
}

// answerSignOutForget records whether the rooms go, then asks whether to sign out.
func (m Model) answerSignOutForget(c confirmState, yes bool) Model {
	m.confirm = confirmState{action: pendingSignOut, leaving: c.leaving, forEveryone: yes}
	return m
}

func (m Model) signOutPrompt(c confirmState) string {
	name := isolate(c.leaving.space.DisplayName())
	rooms := roomsPhrase(len(c.leaving.space.Children))
	if c.action == pendingSignOutForget {
		return "signing out of " + name + ": forget its " + rooms + " and their history here too? no keeps them, readable"
	}
	q := "sign out of " + name + "? its session ends, and :login signs it back in"
	if c.forEveryone {
		q += "; its " + rooms + " and their history go from kith"
	}
	return q
}

type signedOutMsg struct {
	space domain.Space
	err   error
}

func (m Model) signOut(c confirmState) (Model, tea.Cmd) {
	m = m.say("signing out of " + isolate(c.leaving.space.DisplayName()) + "…")
	ctx, backend, space, forget := m.ctx, m.backend, c.leaving.space, c.forEveryone
	return m, func() tea.Msg {
		return signedOutMsg{space: space, err: backend.SignOut(ctx, space.ID, forget)}
	}
}

// handleSignedOut reports the outcome and reloads rooms and spaces.
func (m Model) handleSignedOut(msg signedOutMsg) (Model, tea.Cmd) {
	name := isolate(msg.space.DisplayName())
	if msg.err != nil {
		return m.sayErr("could not sign out of "+name, msg.err), nil
	}
	m = m.say("signed out of " + name)
	return m, tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd())
}
