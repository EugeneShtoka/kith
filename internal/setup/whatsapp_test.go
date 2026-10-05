package setup_test

import (
	"errors"
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

func TestLoginPicksTheNamedOrOnlyAccount(t *testing.T) {
	t.Parallel()
	home := config.WhatsAppAccount{Name: "home", Phone: "+44880000001"}
	work := config.WhatsAppAccount{Name: "work", Phone: "+1500000002"}
	if got, err := setup.WhatsAppAccount(config.WhatsApp{Accounts: []config.WhatsAppAccount{home}}, ""); err != nil || got != home {
		t.Errorf("the only account, unnamed = (%v, %v)", got, err)
	}
	both := config.WhatsApp{Accounts: []config.WhatsAppAccount{home, work}}
	if got, err := setup.WhatsAppAccount(both, "work"); err != nil || got != work {
		t.Errorf("named = (%v, %v)", got, err)
	}
	for _, name := range []string{"", "us"} {
		if _, err := setup.WhatsAppAccount(both, name); !errors.Is(err, setup.ErrNoWhatsAppAccount) {
			t.Errorf("%q among two = %v, want ErrNoWhatsAppAccount", name, err)
		}
	}
}
