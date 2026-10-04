package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Startup and a settings change must derive the same things.
func TestNewDerivesWhatApplyConfigDerives(t *testing.T) {
	t.Parallel()

	display := config.Display{
		Names: []config.DisplayName{
			{Target: "!a:x", Name: "the one room"},
			{Target: config.NameTargetThread + "$t:x", Name: "the thread"},
		},
		Media: config.Media{Audio: config.Audio{Speed: 1.5}},
	}

	fresh, _ := attaching(t)
	fresh = starterNew(fresh.backend, display)

	// The same display, arriving as a settings change instead of at startup.
	changed, _ := attaching(t)
	applied, _ := changed.applyConfig(config.Config{Display: display}, "ok")
	after := applied

	if got, want := fresh.prefs.roomAliases, after.prefs.roomAliases; len(got) != len(want) || got["!a:x"] != want["!a:x"] {
		t.Errorf("roomAliases: New()=%v applyConfig=%v", got, want)
	}
	if got, want := fresh.prefs.threadAliases, after.prefs.threadAliases; len(got) != len(want) {
		t.Errorf("threadAliases: New()=%v applyConfig=%v", got, want)
	}
	if fresh.pics.speed != after.pics.speed {
		t.Errorf("mediaSpeed: New()=%v applyConfig=%v — startup and a settings change disagree",
			fresh.pics.speed, after.pics.speed)
	}
	if fresh.pics.auto != after.pics.auto || fresh.pics.caching != after.pics.caching {
		t.Errorf("media flags differ: New()=(%v,%v) applyConfig=(%v,%v)",
			fresh.pics.auto, fresh.pics.caching, after.pics.auto, after.pics.caching)
	}
}

// A [codes] section WithConfigFile cannot use is reported, and the model falls back.
func TestWithConfigFileReportsABadConfigAndFallsBack(t *testing.T) {
	t.Parallel()

	m, _ := attaching(t)
	m = m.WithConfigFile("/tmp/does-not-matter.toml",
		// max_length below min_length: derivable only as an error.
		config.Config{Codes: config.Codes{MinLength: 8, MaxLength: 2}})

	if m.conf.path == "" {
		t.Error("the config path was dropped along with the bad section")
	}
	if m.st.standing == "" && m.st.event == "" {
		t.Error("a config that could not be derived was applied silently")
	}
}
