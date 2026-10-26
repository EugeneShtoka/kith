package tui

import (
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// Two quick changes save two snapshots on two goroutines; the file ends with the newer.
func TestAnOlderConfigSaveNeverLandsLast(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}}}
	m := starterNew(apitest.Nop{}, config.Display{}).WithConfigFile(path, asStarted(base)).keptIn()

	older, newer := base, base
	older.Notifications.Enabled = false
	newer.Notifications.Enabled = true
	first := m.saveConfigFileCmd(older)
	second := m.saveConfigFileCmd(newer)

	// The newer save finishes first; the older arrives after it.
	for _, cmd := range []func() any{func() any { return second() }, func() any { return first() }} {
		if saved, ok := cmd().(configSavedMsg); !ok || saved.err != nil {
			t.Fatalf("save = %#v, want success", saved)
		}
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Notifications.Enabled {
		t.Error("the older snapshot overwrote the newer one")
	}
}
