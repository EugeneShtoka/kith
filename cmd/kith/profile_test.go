package main

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// --profile takes a profile the config has, in any case, and refuses any other naming
// the ones it has, before the daemon would.
func TestProfileFlagNamesAProfileTheConfigHas(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Profiles: []config.Profile{{Name: "work"}, {Name: "phone"}}}
	backend := daemon.NewRemote("/nonexistent")
	for _, c := range []struct{ name, want string }{{"", ""}, {"work", "work"}, {" Phone ", "phone"}} {
		if got, err := useProfile(backend, cfg, c.name); err != nil || got != c.want {
			t.Errorf("--profile %q = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	_, err := useProfile(backend, cfg, "home")
	if err == nil || !strings.Contains(err.Error(), `"home"`) || !strings.Contains(err.Error(), "work, phone") {
		t.Errorf("--profile home = %v, want it refused naming work, phone", err)
	}
	if _, err := useProfile(backend, config.Config{}, "default"); err != nil {
		t.Errorf("--profile default with no [[profile]]: %v", err)
	}
}
