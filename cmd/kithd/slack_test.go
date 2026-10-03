package main

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/slack"
)

// [[slack.account]] reaches the adapter with its workspace as Slack writes it, and
// [slack] off builds no adapter at all.
func TestSlackAccountsReachTheAdapter(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Slack: config.Slack{Accounts: []config.SlackAccount{{Name: "work", Workspace: "https://Acme.slack.com/"}}}}
	if got := slackAccounts(cfg); len(got) != 1 || got[0] != (slack.Account{Name: "work", Workspace: "acme"}) {
		t.Errorf("slackAccounts = %+v", got)
	}
	if openSlack(nil, nil, cfg, domain.Storage{}) != nil {
		t.Error("[slack] is off, and an adapter was built")
	}
}

// Each session state is a status row under the account's name.
func TestSlackStatus(t *testing.T) {
	t.Parallel()
	account := slack.Account{Name: "work"}
	for s, want := range map[slack.Session]daemon.Phase{
		slack.SignedOut: daemon.PhaseLoggedOut, slack.Connecting: daemon.PhaseConnecting, slack.Connected: daemon.PhaseOnline,
	} {
		got := slackStatus(account, s, "why")
		if got.Phase != want || got.Network != string(domain.ProtocolSlack) || got.Account != "work" || got.Detail != "why" {
			t.Errorf("slackStatus(%v) = %+v, want phase %v", s, got, want)
		}
	}
}
