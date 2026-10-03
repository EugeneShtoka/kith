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
	if got := setup.SlackWorkspace(config.SlackAccount{Workspace: " https://Acme.slack.com/ "}); got != "acme" {
		t.Errorf("SlackWorkspace = %q, want acme", got)
	}
}
