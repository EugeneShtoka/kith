package setup_test

import (
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func TestWhatsAppAccountsAreNamedAndDistinct(t *testing.T) {
	t.Parallel()
	ok := config.WhatsAppAccount{Name: "bg", Phone: "+359 88 000 0001"}
	for name, tc := range map[string]struct {
		accounts []config.WhatsAppAccount
		valid    bool
	}{
		"none":         {nil, true},
		"one":          {[]config.WhatsAppAccount{ok}, true},
		"two":          {[]config.WhatsAppAccount{ok, {Name: "il", Phone: "+972-50-000-0002"}}, true},
		"no name":      {[]config.WhatsAppAccount{{Phone: ok.Phone}}, false},
		"no number":    {[]config.WhatsAppAccount{{Name: "bg", Phone: "+"}}, false},
		"too long":     {[]config.WhatsAppAccount{{Name: "bg", Phone: "1234567890123456"}}, false},
		"name twice":   {[]config.WhatsAppAccount{ok, {Name: "bg", Phone: "+972500000002"}}, false},
		"number twice": {[]config.WhatsAppAccount{ok, {Name: "other", Phone: "359880000001"}}, false},
	} {
		err := setup.WhatsAppAccounts(config.WhatsApp{Accounts: tc.accounts})
		if (err == nil) != tc.valid {
			t.Errorf("%s: WhatsAppAccounts = %v, want valid=%v", name, err, tc.valid)
		}
	}
}

func TestLoginPicksTheNamedOrOnlyAccount(t *testing.T) {
	t.Parallel()
	bg := config.WhatsAppAccount{Name: "bg", Phone: "+359880000001"}
	il := config.WhatsAppAccount{Name: "il", Phone: "+972500000002"}
	if got, err := setup.WhatsAppAccount(config.WhatsApp{Accounts: []config.WhatsAppAccount{bg}}, ""); err != nil || got != bg {
		t.Errorf("the only account, unnamed = (%v, %v)", got, err)
	}
	both := config.WhatsApp{Accounts: []config.WhatsAppAccount{bg, il}}
	if got, err := setup.WhatsAppAccount(both, "il"); err != nil || got != il {
		t.Errorf("named = (%v, %v)", got, err)
	}
	for _, name := range []string{"", "us"} {
		if _, err := setup.WhatsAppAccount(both, name); !errors.Is(err, setup.ErrNoWhatsAppAccount) {
			t.Errorf("%q among two = %v, want ErrNoWhatsAppAccount", name, err)
		}
	}
}
