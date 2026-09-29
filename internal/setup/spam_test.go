package setup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Every check here exists for one reason: a filter that silently did not load is a
// firewall somebody believes is running.
func TestSpamRulesRefuseWhatCannotWork(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spam config.Spam
		want string
	}{
		"a filter with no name": {
			config.Spam{Filters: []config.SpamFilter{{Words: []string{"*bitcoin*"}}}},
			"needs a name",
		},
		"a filter that says nothing": {
			config.Spam{Filters: []config.SpamFilter{{Name: "empty"}}},
			"neither words nor a sender",
		},
		"a share above one": {
			config.Spam{Ratio: 1.5},
			"spam.ratio",
		},
		"a window that is not a duration": {
			config.Spam{Window: "one week"},
			"spam.window",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := setup.SpamRules(tc.spam)
			if err == nil {
				t.Fatalf("SpamRules(%+v) was accepted", tc.spam)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The defaults are what an account that has never thought about this gets: the two
// cheap rules on, the one that can move an established room off, and a week's window.
func TestSpamDefaults(t *testing.T) {
	t.Parallel()

	rules, err := setup.SpamRules(config.Spam{
		Filters: []config.SpamFilter{{Name: "crypto", Words: []string{"*bitcoin*"}}},
	})
	if err != nil {
		t.Fatalf("SpamRules: %v", err)
	}
	if !rules.FirstMessage || !rules.Direct {
		t.Errorf("rules = %+v, want the first-message and direct rules on", rules)
	}
	if rules.Ratio != 0 {
		t.Errorf("ratio = %v, want the rule that moves an established room off by default", rules.Ratio)
	}
	if rules.Window != setup.DefaultSpamWindow || rules.Floor != setup.DefaultSpamFloor {
		t.Errorf("window/floor = %v/%d, want %v/%d",
			rules.Window, rules.Floor, setup.DefaultSpamWindow, setup.DefaultSpamFloor)
	}
	if !rules.Any() {
		t.Error("a configured filter left the rules unable to fire")
	}
	// And with no filter at all, nothing can fire — which is why there is no enabled
	// switch beside them.
	off, err := setup.SpamRules(config.Spam{})
	if err != nil {
		t.Fatalf("SpamRules(empty): %v", err)
	}
	if off.Any() {
		t.Error("an empty filter list can still fire")
	}
}

// A filter that names a sender and no words is a legitimate rule: some spam is known by
// who is saying it rather than by what it says.
func TestASenderIsEnoughForAFilter(t *testing.T) {
	t.Parallel()

	rules, err := setup.SpamRules(config.Spam{
		Filters: []config.SpamFilter{{Name: "that number", From: "@spam:example.org"}},
		Window:  "48h",
	})
	if err != nil {
		t.Fatalf("SpamRules: %v", err)
	}
	if rules.Window != 48*time.Hour {
		t.Errorf("window = %v, want the configured one", rules.Window)
	}
	if _, ok := rules.Catches("hello", "@spam:example.org"); !ok {
		t.Error("a sender-only filter caught nothing from that sender")
	}
	if _, ok := rules.Catches("hello", "@dana:example.org"); ok {
		t.Error("a sender-only filter caught somebody else")
	}
}
