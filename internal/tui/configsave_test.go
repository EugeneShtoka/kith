package tui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// Two quick changes save two snapshots on two goroutines. Whichever finishes last, the
// file holds the newer one.
func TestAnOlderConfigSaveNeverLandsLast(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Homeserver: "https://x", User: "@me:x"}
	m := New(context.Background(), apitest.Nop{}, config.Display{}).WithConfigFile(path, base)

	older, newer := base, base
	older.Notifications.Enabled = false
	newer.Notifications.Enabled = true
	first := m.saveConfigFileCmd(older)
	second := m.saveConfigFileCmd(newer)

	// The newer save wins the race to the disk; the older one arrives after it.
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
