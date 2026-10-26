package tui

import (
	"maps"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// An account that has stopped (signed out, its session refused, failed) is marked on
// its account's rail row, and with the cursor there the status line says why. The
// daemon names accounts by network and config name, the rail's account rows by their
// rooms' owner; an account that falls leaves the daemon's account list on most
// networks, so each one's owner is remembered from when it was listed. One never seen
// listed is the row of its network's only account, when it is the only one.

// downMark leads a stopped account's rail row.
const downMark = "⚠"

// accountsDown is the stopped accounts by their rooms' owner, and each account's
// owner as last listed (by network and name).
type accountsDown struct {
	down   map[domain.RoomOwner]domain.AccountStatus
	owners map[accountKey]domain.RoomOwner
}

type accountKey struct {
	network domain.Protocol
	name    string
}

// accountStatesMsg is the daemon's account states with its account list read beside.
type accountStatesMsg struct {
	states   []domain.AccountStatus
	accounts []domain.Account
	err      error
}

// loadAccountStatesCmd asks the daemon how its accounts are; nil when the backend
// cannot say.
func (m Model) loadAccountStatesCmd() tea.Cmd {
	src, ok := m.backend.(api.AccountStates)
	if !ok {
		return nil
	}
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		states, err := src.AccountStates(ctx)
		if err != nil {
			return accountStatesMsg{err: err}
		}
		accounts, err := backend.Accounts(ctx)
		return accountStatesMsg{states: states, accounts: accounts, err: err}
	}
}

// handleAccountStates keeps which accounts are down; a failed ask keeps the last.
func (m Model) handleAccountStates(msg accountStatesMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m, nil
	}
	owners := make(map[accountKey]domain.RoomOwner, len(m.downAccounts.owners)+len(msg.accounts))
	maps.Copy(owners, m.downAccounts.owners)
	for _, a := range msg.accounts {
		owners[accountKey{a.Network, a.Name}] = a.On
	}
	perNetwork := map[domain.Protocol]int{}
	for _, s := range msg.states {
		perNetwork[s.Network]++
	}
	down := map[domain.RoomOwner]domain.AccountStatus{}
	for _, s := range msg.states {
		if s.Phase != domain.AccountLoggedOut && s.Phase != domain.AccountFailed {
			continue
		}
		on, ok := owners[accountKey{s.Network, s.Account}]
		if !ok && perNetwork[s.Network] == 1 {
			on, ok = m.onlyAccountRow(s.Network)
		}
		if ok {
			down[on] = s
		}
	}
	m.downAccounts = accountsDown{down: down, owners: owners}
	return m, nil
}

// onlyAccountRow is the owner of network's account space, when it has exactly one.
func (m Model) onlyAccountRow(network domain.Protocol) (domain.RoomOwner, bool) {
	var found domain.RoomOwner
	n := 0
	for i := range m.rooms.spaces {
		s := &m.rooms.spaces[i]
		if s.Leaving == domain.LeftBySigningOut && s.Bridge == network {
			found = domain.OwnerOf(domain.RoomID(s.ID))
			n++
		}
	}
	return found, n == 1 && found != ""
}

// accountDown is the account a rail row is, when it has stopped.
func (m Model) accountDown(g group) (domain.AccountStatus, bool) {
	if g.owner == "" {
		return domain.AccountStatus{}, false
	}
	s, ok := m.downAccounts.down[g.owner]
	return s, ok
}

// railAccountWhy says why the account under the rail cursor stopped; "" elsewhere.
func (m Model) railAccountWhy() string {
	if m.focus != paneRail || m.rail.cursor < 0 || m.rail.cursor >= len(m.rail.groups) {
		return ""
	}
	s, ok := m.accountDown(m.rail.groups[m.rail.cursor])
	if !ok {
		return ""
	}
	why := s.Detail
	if why == "" {
		why = "not connected"
	}
	return string(s.Network) + " " + s.Account + " stopped: " + why
}
