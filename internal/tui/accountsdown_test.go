package tui

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountsBackend reports account states, and lists an account only while it is up,
// as most networks do.
type accountsBackend struct {
	apitest.Nop
	states   *[]domain.AccountStatus
	accounts *[]domain.Account
}

func (b accountsBackend) AccountStates(context.Context) ([]domain.AccountStatus, error) {
	return *b.states, nil
}

func (b accountsBackend) Accounts(context.Context) ([]domain.Account, error) { return *b.accounts, nil }

// A stopped account's rail row is marked and, with the cursor on it, the status line
// says why, over any sequence of polls: an account seen up keeps its row when it falls
// out of the account list; one never seen up is its network's only row only when the
// network has one account; an account up again loses the mark.
func TestAStoppedAccountIsMarkedOnItsRow(t *testing.T) {
	t.Parallel()
	type account struct {
		network domain.Protocol
		name    string
		on      domain.RoomOwner
		space   domain.Space
	}
	space := func(network domain.Protocol, id, name string) domain.Space {
		return domain.Space{ID: domain.SpaceID(id), Name: name, Bridge: network, Original: true, Leaving: domain.LeftBySigningOut}
	}
	accounts := []account{
		{domain.ProtocolTelegram, "bg", "telegram:1/", space(domain.ProtocolTelegram, "telegram:1/account", "Telegram bg")},
		{domain.ProtocolTelegram, "il", "telegram:2/", space(domain.ProtocolTelegram, "telegram:2/account", "Telegram il")},
		{domain.ProtocolGMessages, "SMS", "gmessages:g/", space(domain.ProtocolGMessages, "gmessages:g/account", "Google Messages SMS")},
		{domain.ProtocolInstagram, "Eugene", "instagram:9/", space(domain.ProtocolInstagram, "instagram:9/account", "Instagram Eugene")},
	}
	spaces := make([]domain.Space, 0, len(accounts))
	for _, a := range accounts {
		spaces = append(spaces, a.space)
	}
	// Instagram's "me" never logged in: it has no row, and Eugene's is not its.
	never := domain.AccountStatus{Network: domain.ProtocolInstagram, Account: "me", Phase: domain.AccountLoggedOut, Detail: "not signed in"}
	phases := []domain.AccountPhase{domain.AccountLoggedOut, domain.AccountConnecting, domain.AccountOnline, domain.AccountFailed}
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 7))
		var states []domain.AccountStatus
		var listed []domain.Account
		m := New(context.Background(), accountsBackend{states: &states, accounts: &listed}, config.Display{})
		m = sized(t, update(t, m, spacesMsg{spaces: spaces}))
		seen := map[string]bool{}
		for step := range 8 {
			states, listed = []domain.AccountStatus{never}, nil
			down := map[string]domain.AccountStatus{}
			for _, a := range accounts {
				s := domain.AccountStatus{Network: a.network, Account: a.name, Phase: phases[rng.IntN(len(phases))], Detail: "why " + a.name}
				states = append(states, s)
				if s.Phase == domain.AccountLoggedOut || s.Phase == domain.AccountFailed {
					down[a.name] = s
				} else {
					listed = append(listed, domain.Account{Network: a.network, On: a.on, Name: a.name})
				}
			}
			cmd := m.loadAccountStatesCmd()
			if cmd == nil {
				t.Fatal("a backend that reports account states was not asked")
			}
			m = update(t, m, cmd())
			for _, a := range listed {
				seen[a.Name] = true
			}
			m.focus = paneRail
			for _, a := range accounts {
				_, isDown := down[a.name]
				// Telegram has two accounts, Instagram two states: only GMessages has one.
				want := isDown && (seen[a.name] || a.network == domain.ProtocolGMessages)
				i := indexOfGroup(m.rail.groups, a.space.Name)
				if i < 0 {
					t.Fatalf("seed %d: no rail row %q", seed, a.space.Name)
				}
				m.rail.cursor = i
				row := m.railRow(m.rail.groups[i], true, true, 40)
				why := m.railAccountWhy()
				if strings.Contains(row, downMark) != want || (why != "") != want {
					t.Fatalf("seed %d step %d: %s down=%v seen=%v: row %q, why %q; want marked %v",
						seed, step, a.name, isDown, seen[a.name], row, why, want)
				}
				if want && !strings.Contains(why, "why "+a.name) {
					t.Fatalf("seed %d step %d: why %q does not say the account's detail", seed, step, why)
				}
			}
		}
	}
}
