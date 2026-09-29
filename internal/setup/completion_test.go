package setup

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// A duration somebody wrote wrongly is refused where a typo can be reported with its
// key, which is the half of the contract the OrDefault helpers cannot keep: they
// resolve, and resolving quietly is how a setting somebody believes they changed stays
// unchanged.
func TestModelDurationsRefuseWhatCannotBeRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  config.CompleteModel
		want string
	}{
		{"unparseable idle", config.CompleteModel{Idle: "ten minutes"}, "complete.model.idle"},
		{"negative startup", config.CompleteModel{Startup: "-5s"}, "complete.model.startup"},
		{"a timeout of nothing", config.CompleteModel{Timeout: "0s"}, "complete.model.timeout"},
	} {
		if err := ModelDurations(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %s", tc.name, err, tc.want)
		}
	}
	// And the one place zero is an answer rather than an omission: never stop the
	// server is a thing somebody may legitimately want to say.
	if err := ModelDurations(config.CompleteModel{Idle: "0s"}); err != nil {
		t.Errorf(`idle = "0s" was refused: %v`, err)
	}
	if err := ModelDurations(config.CompleteModel{}); err != nil {
		t.Errorf("an untouched section was refused: %v", err)
	}
}

// A bare word is refused, because with one list it could mean three things.
func TestANameTargetHasToSayWhatItNames(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"!abc:example.org", config.NameTargetRoom + "Standup",
		config.NameTargetSpace + "Work", config.NameTargetGroup + "unread",
		config.NameTargetThread + "$evt",
	} {
		if err := NameTargets(config.Display{
			Names: []config.DisplayName{{Target: target, Name: "x"}},
		}); err != nil {
			t.Errorf("%q was refused: %v", target, err)
		}
	}
	for _, target := range []string{"Standup", "", "   "} {
		err := NameTargets(config.Display{
			Names: []config.DisplayName{{Target: target, Name: "x"}},
		})
		if err == nil {
			t.Errorf("%q was accepted, so a typo would silently never match", target)
		}
	}
}
