package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

func TestEverySettingChoiceIsAcceptedByTheApp(t *testing.T) {
	t.Parallel()

	base := func() config.Settings {
		cfg := config.Config{Profiles: starter(t).Profiles, Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://example.org", User: "@me:example.org"}}}}
		return config.Settings{Bridge: cfg, Device: config.Device{Keys: config.DefaultKeys()}}
	}

	checked := 0
	for _, setting := range settingsList {
		if setting.kind != settingChoice || setting.set == nil {
			continue
		}
		for _, choice := range setting.choices {
			cfg := base()
			if err := setting.set(&cfg, choice.value); err != nil {
				// A refusal from set() is a precondition, not a bad vocabulary.
				continue
			}
			checked++
			if _, err := derive(cfg, ""); err != nil {
				t.Errorf("%s offers %q, and applying it fails:\n  %v",
					setting.key, choice.value, err)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no choice settings were exercised; the walk over settingsList is broken")
	}
}
