package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountThenKind is what waits on an account being picked.
type accountThenKind int

const (
	// thenOpenLinked opens the chat a Telegram or WhatsApp link named, as the picked
	// account sees it.
	thenOpenLinked accountThenKind = iota + 1
	// thenOpenPlace opens the Matrix room a link named, as the picked account sees it.
	thenOpenPlace
	// thenResolve asks the picked account's network for the chat a link names.
	thenResolve
	// thenJoin joins a Matrix room by an address as Matrix writes it.
	thenJoin
)

// accountThen is a call that names no account, waiting for one (domain.PickAccount):
// what it does once the account is known.
type accountThen struct {
	kind    accountThenKind
	network domain.Protocol
	// rooms is the chat as each account already in it sees it (the open kinds).
	rooms []domain.Room
	// post and text are a link's post and suggested message; event a Matrix link's.
	post  int
	text  string
	event domain.EventID
	// uri is the link to look up (thenResolve); address and via the room to join
	// (thenJoin).
	uri     string
	address string
	via     []string
}

// accountListMsg is every connected account, for the cache (loadAccountsCmd).
type accountListMsg struct {
	accounts []domain.Account
	err      error
}

// accountsPickedMsg is every connected account, asked for to pick one for then.
type accountsPickedMsg struct {
	then     accountThen
	accounts []domain.Account
	err      error
}

// loadAccountsCmd asks which accounts are connected: at start, and when rooms show a
// new account (selvesAfterRooms).
func (m Model) loadAccountsCmd() tea.Cmd {
	return fetch(m.ctx, m.backend.Accounts, func(accounts []domain.Account, err error) tea.Msg {
		return accountListMsg{accounts: accounts, err: err}
	})
}

// handleAccountList keeps the accounts; a failed ask keeps the last ones.
func (m Model) handleAccountList(msg accountListMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		m.accounts = msg.accounts
	}
	return m, nil
}

// pickAccount runs then on the account the rule picks: one already in the chat, else
// the network's only account. Anything else needs the defaults as they are now, so
// the daemon is asked first (handleAccountsPicked).
func (m Model) pickAccount(then accountThen) (Model, tea.Cmd) {
	in := thenOwners(then)
	if len(in) == 1 {
		return m.withAccount(then, domain.Account{Network: then.network, On: in[0]})
	}
	if len(in) == 0 {
		var mine []domain.Account
		for _, a := range m.accounts {
			if a.Network == then.network {
				mine = append(mine, a)
			}
		}
		if len(mine) == 1 {
			return m.withAccount(then, mine[0])
		}
	}
	return m, fetch(m.ctx, m.backend.Accounts, func(accounts []domain.Account, err error) tea.Msg {
		return accountsPickedMsg{then: then, accounts: accounts, err: err}
	})
}

// thenOwners is the accounts already in the chat then is about.
func thenOwners(then accountThen) []domain.RoomOwner {
	var in []domain.RoomOwner
	for i := range then.rooms {
		if owner := domain.OwnerOf(then.rooms[i].ID); !slices.Contains(in, owner) {
			in = append(in, owner)
		}
	}
	return in
}

// handleAccountsPicked runs the waiting call on the account the rule picks, or asks
// which.
func (m Model) handleAccountsPicked(msg accountsPickedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not tell which account to use", msg.err), nil
	}
	m.accounts = msg.accounts
	picked, ask := domain.PickAccount(msg.accounts, msg.then.network, thenOwners(msg.then))
	switch {
	case picked.On != "":
		return m.withAccount(msg.then, picked)
	case len(ask) == 0:
		return m.say("no " + msg.then.network.String() + " account is connected"), nil
	}
	items := make([]pickerItem, 0, len(ask))
	for _, a := range ask {
		items = append(items, pickerItem{label: a.Name, value: string(a.On), match: a.Name})
	}
	spec := pickerSpecs[pickerChooseAccount]
	spec.title = msg.then.network.String() + ": which account?"
	m.picker = newPickerWith(pickerChooseAccount, spec, items)
	m.pickingAccount = msg.then
	return m, nil
}

// chooseAccount runs the waiting call on the account chosen.
func (m Model) chooseAccount(on string) (Model, tea.Cmd) {
	m = m.closePicker()
	then := m.pickingAccount
	m.pickingAccount = accountThen{}
	return m.withAccount(then, domain.Account{Network: then.network, On: domain.RoomOwner(on)})
}

// withAccount runs then on account.
func (m Model) withAccount(then accountThen, account domain.Account) (Model, tea.Cmd) {
	room := func() (domain.Room, bool) {
		i := slices.IndexFunc(then.rooms, func(r domain.Room) bool { return domain.OwnerOf(r.ID) == account.On })
		if i < 0 {
			return domain.Room{}, false
		}
		return then.rooms[i], true
	}
	switch then.kind {
	case thenOpenLinked:
		if r, ok := room(); ok {
			return m.openLinked(r, then.post, then.text)
		}
	case thenOpenPlace:
		if r, ok := room(); ok {
			return m.openPlaceRoom(r, then.event)
		}
	case thenResolve:
		return m.doing("looking up " + then.uri + "…"), m.resolveLinkCmd(then.uri, account.On, then.post, then.text)
	case thenJoin:
		m = m.say("joining " + then.address + "…")
		return m, m.joinRoomCmd(string(account.On)+then.address, then.via...)
	}
	return m, nil
}
