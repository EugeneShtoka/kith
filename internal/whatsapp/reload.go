package whatsapp

import (
	"context"
	"slices"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// UseConfig applies [[whatsapp.account]] and [display.deleted] keep, at start and on
// each reload.
func (a *Adapter) UseConfig(ctx context.Context, cfg config.Config) {
	a.keepDeletedIf(cfg.Display.Deleted.Keep())
	accounts := make([]Account, 0, len(cfg.WhatsApp.Accounts))
	for _, account := range cfg.WhatsApp.Accounts {
		accounts = append(accounts, Account{Name: account.Name, Digits: domain.PhoneDigits(account.Phone)})
	}
	a.useAccounts(ctx, accounts)
}

// CheckConfig validates [[whatsapp.account]]s offline: unique names, international
// numbers, no duplicates.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.WhatsApp.Accounts))
	for _, account := range cfg.WhatsApp.Accounts {
		records = append(records, domain.PhoneAccount(account.Name, account.Phone))
	}
	return domain.CheckAccounts(cfg.WhatsApp.Table(), records) //nolint:wrapcheck // names the record itself
}

// useAccounts applies the account list. After Start, an added linked account
// connects, an unlinked one can be paired, and a removed one disconnects (its cached
// rooms stay; unlinking is done on the phone).
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
