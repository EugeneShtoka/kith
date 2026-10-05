package main

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/telegram"
)

// [[telegram.account]] reaches the adapter by its number's digits, and a config with
// none builds no adapter at all: Telegram runs when it has an account.
func TestTelegramAccountsReachTheAdapter(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Telegram: config.Telegram{Accounts: []config.TelegramAccount{{Name: "home", Phone: "+44 7700 900000"}}}}
	if got := telegramAccounts(cfg); len(got) != 1 || got[0] != (telegram.Account{Name: "home", Digits: "447700900000"}) {
		t.Errorf("telegramAccounts = %+v", got)
	}
	if openTelegram(nil, nil, config.Config{}, domain.Storage{}) != nil {
		t.Error("no [[telegram.account]], and an adapter was built")
	}
	if openTelegram(nil, nil, cfg, domain.Storage{}) == nil {
		t.Error("a [[telegram.account]], and no adapter")
	}
}

// Each session state is a status row under the account's name.
func TestTelegramStatus(t *testing.T) {
	t.Parallel()
	account := telegram.Account{Name: "home"}
	for s, want := range map[telegram.Session]daemon.Phase{
		telegram.LoggedOut: daemon.PhaseLoggedOut, telegram.Connecting: daemon.PhaseConnecting, telegram.Connected: daemon.PhaseOnline,
	} {
		got := telegramStatus(account, s, "why")
		if got.Phase != want || got.Network != string(domain.ProtocolTelegram) || got.Account != "home" || got.Detail != "why" {
			t.Errorf("telegramStatus(%v) = %+v, want phase %v", s, got, want)
		}
	}
}
