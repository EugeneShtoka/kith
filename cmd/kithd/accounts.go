package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountDefaults is each network's [<network>] default, as the config last read says.
type accountDefaults struct {
	mu sync.Mutex
	by map[domain.Protocol]string
}

// use takes cfg's defaults.
func (d *accountDefaults) use(cfg config.Config) {
	by := map[domain.Protocol]string{}
	for _, n := range cfg.Networks() {
		by[domain.Protocol(n.Network())] = n.DefaultAccount()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.by = by
}

// mark sets Default on each account its network's default names.
func (d *accountDefaults) mark(accounts []domain.Account) []domain.Account {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range accounts {
		name, ok := d.by[accounts[i].Network]
		accounts[i].Default = ok && name != "" && name == accounts[i].Name
	}
	return accounts
}

// Accounts is every connected account, each network's default marked.
func (s served) Accounts(ctx context.Context) ([]domain.Account, error) {
	accounts, err := s.Router.Accounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return s.defaults.mark(accounts), nil
}
