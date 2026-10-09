package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving an account's own space from the rail signs the account out: its session
// ends on the network and its kept session is deleted, while the account stays
// configured (:login signs it back in). Whether its rooms and their history go from
// kith too is asked first (no keeps them, readable); last, saying what goes, whether to
// sign out. Nothing happens before that last yes.

// askSignOut opens the questions for signing out the account space is.
func (m Model) askSignOut(space domain.Space) Model {
	m.confirm = confirmState{action: pendingSignOutForget, leaving: spaceLeave{space: space}}
	return m
}

// answerSignOutForget takes whether the account's rooms go too, and asks last whether
// to sign out.
func (m Model) answerSignOutForget(c confirmState, yes bool) Model {
	m.confirm = confirmState{action: pendingSignOut, leaving: c.leaving, forEveryone: yes}
	return m
}

// signOutPrompt is the question for each step of signing out.
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

// signedOutMsg reports signing out the account a space is.
type signedOutMsg struct {
	space domain.Space
	err   error
}

// signOut signs out the account the questions settled on.
func (m Model) signOut(c confirmState) (Model, tea.Cmd) {
	m = m.say("signing out of " + isolate(c.leaving.space.DisplayName()) + "…")
	ctx, backend, space, forget := m.ctx, m.backend, c.leaving.space, c.forEveryone
	return m, func() tea.Msg {
		return signedOutMsg{space: space, err: backend.SignOut(ctx, space.ID, forget)}
	}
}

// handleSignedOut says how signing out went, and reads the rooms and spaces again.
func (m Model) handleSignedOut(msg signedOutMsg) (Model, tea.Cmd) {
	name := isolate(msg.space.DisplayName())
	if msg.err != nil {
		return m.sayErr("could not sign out of "+name, msg.err), nil
	}
	m = m.say("signed out of " + name)
	return m, tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd())
}
