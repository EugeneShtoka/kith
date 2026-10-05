package main

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
)

// networks is a daemon that lists logins and does nothing else.
type networks struct {
	apitest.Nop
	list []api.LoginNetwork
}

func (n networks) LoginNetworks(context.Context) ([]api.LoginNetwork, error) { return n.list, nil }
func (networks) BeginLogin(context.Context, string, string) (api.LoginStep, error) {
	return api.LoginStep{}, nil
}
func (networks) AnswerLogin(context.Context, string, map[string]string) (api.LoginStep, error) {
	return api.LoginStep{}, nil
}
func (networks) CancelLogin(context.Context, string) error { return nil }

// `kith login <network> [account]` means the account named, or the network's only
// one, or a new one when it has none; two unnamed, or a name it lacks, are refused.
func TestLoginPicksTheNamedOrOnlyAccount(t *testing.T) {
	t.Parallel()
	d := networks{list: []api.LoginNetwork{
		{Network: "one", Accounts: []api.LoginAccount{{Name: "home"}}},
		{Network: "two", Accounts: []api.LoginAccount{{Name: "home"}, {Name: "work"}}},
		{Network: "none"},
	}}
	ctx := t.Context()
	for _, c := range []struct{ network, named, want, refused string }{
		{"one", "", "home", ""},
		{"two", "work", "work", ""},
		{"none", "", "", ""},
		{"two", "", "", "say which"},
		{"one", "work", "", "no account called work"},
		{"three", "", "", "no network is called three"},
	} {
		got, err := loginAccount(ctx, d, c.network, c.named)
		if c.refused != "" {
			if err == nil || !strings.Contains(err.Error(), c.refused) {
				t.Errorf("%s %q = (%q, %v), want refused saying %q", c.network, c.named, got, err, c.refused)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s %q = (%q, %v), want %q", c.network, c.named, got, err, c.want)
		}
	}
}
