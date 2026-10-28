package setup

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Misspelled durations are refused with their key instead of silently defaulting.
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
	// Idle "0s" is valid: never stop the server.
	if err := ModelDurations(config.CompleteModel{Idle: "0s"}); err != nil {
		t.Errorf(`idle = "0s" was refused: %v`, err)
	}
	if err := ModelDurations(config.CompleteModel{}); err != nil {
		t.Errorf("an untouched section was refused: %v", err)
	}
}

// A bare target is refused: it could be a room, space or thread.
func TestANameTargetHasToSayWhatItNames(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		mx + "!abc:example.org", config.NameTargetRoom + "Standup",
		config.NameTargetSpace + "Work",
		config.NameTargetThread + mx + "$evt",
		"whatsapp:44000000001/120363000000000001@g.us",
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
