package setup

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// A Telegram account needs a name and an international number, neither repeated;
// login picks one by name, or the only one.
func TestTelegramAccounts(t *testing.T) {
	t.Parallel()
	home := config.TelegramAccount{Name: "home", Phone: "+44 7700 900000"}
	work := config.TelegramAccount{Name: "work", Phone: "+1 202 555 0100"}
	for name, c := range map[string]struct {
		accounts []config.TelegramAccount
		ok       bool
	}{
		"two":         {[]config.TelegramAccount{home, work}, true},
		"none":        {nil, true},
		"no name":     {[]config.TelegramAccount{{Phone: home.Phone}}, false},
		"short":       {[]config.TelegramAccount{{Name: "x", Phone: "+12"}}, false},
		"name twice":  {[]config.TelegramAccount{home, {Name: "home", Phone: work.Phone}}, false},
		"phone twice": {[]config.TelegramAccount{home, {Name: "other", Phone: "+447700900000"}}, false},
	} {
		if err := TelegramAccounts(config.Telegram{Accounts: c.accounts}); (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok %v", name, err, c.ok)
		}
	}
}
