package setup_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// A network's default names one of its accounts; none is fine.
func TestADefaultNamesOneOfTheAccounts(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Telegram: config.Telegram{Accounts: []config.TelegramAccount{{Name: "home"}, {Name: "work"}}}}
	if err := setup.DefaultAccounts(cfg); err != nil {
		t.Errorf("no default: %v", err)
	}
	cfg.Telegram.Default = "work"
	if err := setup.DefaultAccounts(cfg); err != nil {
		t.Errorf("default = work: %v", err)
	}
	cfg.Telegram.Default = "club"
	if err := setup.DefaultAccounts(cfg); err == nil || !strings.Contains(err.Error(), "telegram.account") {
		t.Errorf("default = club: %v, want it refused naming the table", err)
	}
}
