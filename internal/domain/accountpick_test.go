package domain_test

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The account a call naming none goes to: one already in the chat; of several in it,
// the default if it is one of them, else ask among them; with none in it, the only
// account, else the default, else ask among all.
func TestPickAccountTakesTheJoinedThenTheDefaultThenAsks(t *testing.T) {
	t.Parallel()

	home := domain.Account{Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, "1"), Name: "home"}
	work := domain.Account{Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, "2"), Name: "work"}
	club := domain.Account{Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, "3"), Name: "club"}
	slack := domain.Account{Network: domain.ProtocolSlack, On: domain.AccountRooms(domain.ProtocolSlack, "T1"), Name: "team"}
	dflt := func(a domain.Account) domain.Account { a.Default = true; return a }
	names := func(as []domain.Account) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.Name)
		}
		return out
	}

	for name, tc := range map[string]struct {
		accounts []domain.Account
		in       []domain.RoomOwner
		picked   string
		ask      []string
	}{
		"the only account":                         {[]domain.Account{home, slack}, nil, "home", nil},
		"the default, none in the chat":            {[]domain.Account{home, dflt(work), slack}, nil, "work", nil},
		"ask, none in the chat, no default":        {[]domain.Account{home, work, slack}, nil, "", []string{"home", "work"}},
		"the one in the chat, over the default":    {[]domain.Account{home, dflt(work)}, []domain.RoomOwner{home.On}, "home", nil},
		"the default among those in the chat":      {[]domain.Account{home, dflt(work), club}, []domain.RoomOwner{home.On, work.On}, "work", nil},
		"ask among those in it, the default isn't": {[]domain.Account{home, work, dflt(club)}, []domain.RoomOwner{home.On, work.On}, "", []string{"home", "work"}},
		"another network's room counts for none":   {[]domain.Account{home, dflt(work)}, []domain.RoomOwner{slack.On}, "work", nil},
		"no account on the network":                {[]domain.Account{slack}, nil, "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			picked, ask := domain.PickAccount(tc.accounts, domain.ProtocolWhatsApp, tc.in)
			if picked.Name != tc.picked || !slices.Equal(names(ask), tc.ask) {
				t.Errorf("picked %q, ask %v; want %q, %v", picked.Name, names(ask), tc.picked, tc.ask)
			}
		})
	}
}
