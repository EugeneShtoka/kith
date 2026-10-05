package whatsapp

import (
	"context"
	"slices"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// UseConfig takes [[whatsapp.account]] and [display.deleted] keep, at start and each
// time the config is re-read (see useAccounts).
func (a *Adapter) UseConfig(ctx context.Context, cfg config.Config) {
	a.keepDeletedIf(cfg.Display.Deleted.Keep())
	accounts := make([]Account, 0, len(cfg.WhatsApp.Accounts))
	for _, account := range cfg.WhatsApp.Accounts {
		accounts = append(accounts, Account{Name: account.Name, Digits: domain.PhoneDigits(account.Phone)})
	}
	a.useAccounts(ctx, accounts)
}

// CheckConfig refuses [[whatsapp.account]]s kith could not tell apart or link: a
// missing or repeated name, no international number, a number listed twice. It asks
// nothing of WhatsApp.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.WhatsApp.Accounts))
	for _, account := range cfg.WhatsApp.Accounts {
		records = append(records, domain.PhoneAccount(account.Name, account.Phone))
	}
	return domain.CheckAccounts(cfg.WhatsApp.Table(), records) //nolint:wrapcheck // names the record itself
}

// useAccounts takes the [[whatsapp.account]] list: before Start, the ones it starts
// with; after, without a restart, an account added that is already linked connects,
// one added that is not can be paired at once, and one removed is disconnected. A
// removed account's rooms stay in the cache, readable; unlinking it is the phone's to
// do.
func (a *Adapter) useAccounts(ctx context.Context, accounts []Account) {
	a.mu.Lock()
	previous := a.accounts
	a.accounts = slices.Clone(accounts)
	started, stopped := a.started, a.stopped
	var gone []string
	for _, old := range previous {
		if !slices.ContainsFunc(accounts, func(acc Account) bool { return acc.Digits == old.Digits }) {
			gone = append(gone, old.Digits)
		}
	}
	for _, digits := range gone {
		if client := a.clients[digits]; client != nil {
			delete(a.clients, digits)
			go client.Disconnect() // its handler may be waiting on this lock
		}
	}
	a.mu.Unlock()
	for _, old := range previous {
		if slices.Contains(gone, old.Digits) {
			a.link(old, Unlinked, "no longer in the config")
		}
	}
	if !started || stopped {
		return // Start connects whatever the list holds then
	}
	for _, account := range accounts {
		if slices.ContainsFunc(previous, func(old Account) bool { return old.Digits == account.Digits }) {
			continue
		}
		if err := a.connectLinked(ctx, account); err != nil {
			a.log.Warn("connect an added account failed", "account", account.Name, "err", err)
		}
	}
}
