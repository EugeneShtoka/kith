package whatsapp

import (
	"context"
	"slices"
)

// UseAccounts takes a changed [[whatsapp.account]] list without a restart: an account
// added that is already linked connects, one added that is not can be paired at once,
// and one removed is disconnected. A removed account's rooms stay in the cache,
// readable; unlinking it is the phone's to do.
func (a *Adapter) UseAccounts(ctx context.Context, accounts []Account) {
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
