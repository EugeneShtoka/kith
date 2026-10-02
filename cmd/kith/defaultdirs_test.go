package main

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// The packaged units let the daemon write only the default directories: a config
// keeping any of its files elsewhere needs a unit of its own.
func TestInDefaultDirs(t *testing.T) {
	t.Parallel()
	def, err := setup.StorageDirs(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !inDefaultDirs(def) {
		t.Errorf("inDefaultDirs(defaults) = false, want true")
	}
	def.Instance, def.KeyringService = "other", "other"
	if !inDefaultDirs(def) {
		t.Errorf("inDefaultDirs(defaults, another instance and keyring) = false, want true: the units don't name either")
	}
	for name, move := range map[string]func(s *config.Storage){
		"data":    func(s *config.Storage) { s.DataDir = "/elsewhere/data" },
		"state":   func(s *config.Storage) { s.StateDir = "/elsewhere/state" },
		"cache":   func(s *config.Storage) { s.CacheDir = "/elsewhere/cache" },
		"runtime": func(s *config.Storage) { s.RuntimeDir = "/elsewhere/run" },
	} {
		var cfg config.Config
		move(&cfg.Storage)
		storage, err := setup.StorageDirs(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if inDefaultDirs(storage) {
			t.Errorf("inDefaultDirs(%s moved) = true, want false", name)
		}
	}
}
