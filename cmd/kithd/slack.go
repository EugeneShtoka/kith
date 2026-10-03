package main

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/slack"

	"log/slog"
)

// openSlack is the Slack adapter when [slack] is enabled, its credentials in this
// instance's keyring entries.
func openSlack(cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage) *slack.Adapter {
	if !cfg.Slack.Enabled {
		return nil
	}
	secrets := keyringSecrets{service: storage.KeyringService, scope: storage.Instance}
	return slack.New(cache, secrets, slackAccounts(cfg), log)
}

// slackAccounts is [[slack.account]] as the adapter takes it.
func slackAccounts(cfg config.Config) []slack.Account {
	accounts := make([]slack.Account, 0, len(cfg.Slack.Accounts))
	for _, a := range cfg.Slack.Accounts {
		accounts = append(accounts, slack.Account{Name: a.Name, Workspace: setup.SlackWorkspace(a)})
	}
	return accounts
}

// keyringSecrets keeps a network's credentials in the OS secret store, under this
// instance: two instances signed in to one workspace hold two sessions.
type keyringSecrets struct {
	service, scope string
}

func (k keyringSecrets) ref(ref string) string { return k.scope + "|" + ref }

// Secret is the value kept under ref; ok false when there is none.
func (k keyringSecrets) Secret(ref string) (string, bool, error) {
	value, err := session.Secret(k.service, k.ref(ref))
	return value, value != "", err //nolint:wrapcheck // session's error says what is missing
}

// StoreSecret keeps value under ref.
func (k keyringSecrets) StoreSecret(ref, value string) error {
	return session.StoreSecret(k.service, k.ref(ref), value) //nolint:wrapcheck // as Secret
}

// DeleteSecret removes what is kept under ref.
func (k keyringSecrets) DeleteSecret(ref string) error {
	return session.DeleteSecret(k.service, k.ref(ref)) //nolint:wrapcheck // as Secret
}

// slackStatus is a Slack account's row in the daemon's status.
func slackStatus(account slack.Account, s slack.Session, detail string) daemon.NetworkStatus {
	phase := daemon.PhaseLoggedOut
	switch s {
	case slack.Connecting:
		phase = daemon.PhaseConnecting
	case slack.Connected:
		phase = daemon.PhaseOnline
	case slack.SignedOut:
	}
	return daemon.NetworkStatus{Network: string(domain.ProtocolSlack), Account: account.Name, Phase: phase, Detail: detail}
}
