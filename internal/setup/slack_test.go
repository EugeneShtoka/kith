package setup_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func TestSlackAccountsAreNamedAndDistinct(t *testing.T) {
	t.Parallel()
	ok := config.SlackAccount{Name: "work", Workspace: "acme"}
	for name, tc := range map[string]struct {
		accounts []config.SlackAccount
		valid    bool
	}{
		"none":            {nil, true},
		"one":             {[]config.SlackAccount{ok}, true},
		"full address":    {[]config.SlackAccount{{Name: "work", Workspace: "https://Acme-Co.slack.com/"}}, true},
		"two":             {[]config.SlackAccount{ok, {Name: "club", Workspace: "chess-club.slack.com"}}, true},
		"team ID":         {[]config.SlackAccount{{Name: "work", Workspace: "T0000000AAA"}}, true},
		"client link":     {[]config.SlackAccount{{Name: "work", Workspace: "https://app.slack.com/client/T0000000AAA/D0000000BBB"}}, true},
		"lower-case ID":   {[]config.SlackAccount{{Name: "work", Workspace: "t0000000aaa"}}, true}, // reads as an address
		"bad link":        {[]config.SlackAccount{{Name: "work", Workspace: "https://app.slack.com/client/"}}, false},
		"no name":         {[]config.SlackAccount{{Workspace: "acme"}}, false},
		"no workspace":    {[]config.SlackAccount{{Name: "work"}}, false},
		"not an address":  {[]config.SlackAccount{{Name: "work", Workspace: "Acme Inc"}}, false},
		"name twice":      {[]config.SlackAccount{ok, {Name: "work", Workspace: "other"}}, false},
		"workspace twice": {[]config.SlackAccount{ok, {Name: "again", Workspace: "ACME.slack.com"}}, false},
	} {
		err := setup.SlackAccounts(config.Slack{Accounts: tc.accounts})
		if (err == nil) != tc.valid {
			t.Errorf("%s: SlackAccounts = %v, want valid=%v", name, err, tc.valid)
		}
	}
	for in, want := range map[string]string{
		"\thttps://Acme.slack.com/\n": "acme",
		"T0000000AAA":                 "T0000000AAA",
		"https://app.slack.com/client/T0000000AAA/D000000BBB": "T0000000AAA",
		"app.slack.com/client/T0000000AAA":                    "T0000000AAA",
	} {
		if got := (config.SlackAccount{Workspace: in}).Address(); got != want {
			t.Errorf("Address(%q) = %q, want %q", in, got, want)
		}
	}
}
