package main

import (
	"log/slog"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/telegram"
)

// openTelegram is the Telegram adapter when the config has a [[telegram.account]],
// its credentials in this instance's keyring entries.
func openTelegram(cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage) *telegram.Adapter {
	if len(cfg.Telegram.Accounts) == 0 {
		return nil
	}
	secrets := keyringSecrets{service: storage.KeyringService, scope: storage.Instance}
	return telegram.New(cache, secrets, telegramAccounts(cfg), log)
}

// telegramAccounts is [[telegram.account]] as the adapter takes it.
func telegramAccounts(cfg config.Config) []telegram.Account {
	accounts := make([]telegram.Account, 0, len(cfg.Telegram.Accounts))
	for _, a := range cfg.Telegram.Accounts {
		accounts = append(accounts, telegram.Account{Name: a.Name, Digits: a.Digits()})
	}
	return accounts
}

// telegramStatus is a Telegram account's row in the daemon's status.
func telegramStatus(account telegram.Account, s telegram.Session, detail string) daemon.NetworkStatus {
	phase := daemon.PhaseLoggedOut
	switch s {
	case telegram.Connecting:
		phase = daemon.PhaseConnecting
	case telegram.Connected:
		phase = daemon.PhaseOnline
	case telegram.LoggedOut:
	}
	return daemon.NetworkStatus{Network: string(domain.ProtocolTelegram), Account: account.Name, Phase: phase, Detail: detail}
}
