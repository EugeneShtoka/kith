package setup_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func TestWhatsAppAccountsAreNamedAndDistinct(t *testing.T) {
	t.Parallel()
	ok := config.WhatsAppAccount{Name: "home", Phone: "+44 7700 900001"}
	for name, tc := range map[string]struct {
		accounts []config.WhatsAppAccount
		valid    bool
	}{
		"none":         {nil, true},
		"one":          {[]config.WhatsAppAccount{ok}, true},
		"two":          {[]config.WhatsAppAccount{ok, {Name: "work", Phone: "+1-202-555-0102"}}, true},
		"no name":      {[]config.WhatsAppAccount{{Phone: ok.Phone}}, false},
		"no number":    {[]config.WhatsAppAccount{{Name: "home", Phone: "+"}}, false},
		"too long":     {[]config.WhatsAppAccount{{Name: "home", Phone: "1234567890123456"}}, false},
		"name twice":   {[]config.WhatsAppAccount{ok, {Name: "home", Phone: "+1500000002"}}, false},
		"number twice": {[]config.WhatsAppAccount{ok, {Name: "other", Phone: "447700900001"}}, false},
	} {
		err := setup.WhatsAppAccounts(config.WhatsApp{Accounts: tc.accounts})
		if (err == nil) != tc.valid {
			t.Errorf("%s: WhatsAppAccounts = %v, want valid=%v", name, err, tc.valid)
		}
	}
}
