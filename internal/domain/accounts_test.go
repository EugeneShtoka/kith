package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An account a phone number names needs a name and an international number, neither
// repeated: login picks one by name, and the number is whom it logs in as.
func TestPhoneAccountsAreNamedAndDistinct(t *testing.T) {
	t.Parallel()
	type acc struct{ name, phone string }
	home, work := acc{"home", "+44 7700 900001"}, acc{"work", "+1-202-555-0102"}
	for name, tc := range map[string]struct {
		accounts []acc
		valid    bool
	}{
		"none":         {nil, true},
		"one":          {[]acc{home}, true},
		"two":          {[]acc{home, work}, true},
		"no name":      {[]acc{{"", home.phone}}, false},
		"no number":    {[]acc{{"home", "+"}}, false},
		"short":        {[]acc{{"home", "+12"}}, false},
		"too long":     {[]acc{{"home", "1234567890123456"}}, false},
		"name twice":   {[]acc{home, {"home", work.phone}}, false},
		"number twice": {[]acc{home, {"other", "447700900001"}}, false},
	} {
		records := make([]domain.AccountRecord, 0, len(tc.accounts))
		for _, a := range tc.accounts {
			records = append(records, domain.PhoneAccount(a.name, a.phone))
		}
		if err := domain.CheckAccounts("x.account", records); (err == nil) != tc.valid {
			t.Errorf("%s: CheckAccounts = %v, want valid=%v", name, err, tc.valid)
		}
	}
}
