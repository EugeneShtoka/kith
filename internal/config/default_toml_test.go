package config

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The starter config parses and its documented values equal the built-in defaults.
func TestShippedDefaultTOMLMatchesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := WriteDefaultIfMissing(path); err != nil {
		t.Fatalf("WriteDefaultIfMissing: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped default config does not load: %v", err)
	}
	devicePath := DevicePath(path)
	if _, werr := WriteDeviceDefaultIfMissing(devicePath); werr != nil {
		t.Fatalf("WriteDeviceDefaultIfMissing: %v", werr)
	}
	dev, err := LoadDevice(devicePath)
	if err != nil {
		t.Fatalf("the shipped default device file does not load: %v", err)
	}
	if !reflect.DeepEqual(dev.Keys, DefaultKeys()) {
		t.Errorf("shipped [keys] differs from DefaultKeys()\n got %+v\nwant %+v", dev.Keys, DefaultKeys())
	}
	// The code-detection section's stated defaults match the code.
	c := cfg.Codes
	if c.Min() != 4 || c.Max() != 8 || !c.LettersAllowed() || !c.DigitsAllowed() || !c.DigitRequired() {
		t.Errorf("shipped [codes] = %+v, want 4-8 alnum with a digit required", c)
	}
	if c.Symbols != "" || len(c.Include) != 0 || len(c.Exclude) != 0 {
		t.Errorf("shipped [codes] should ship empty lists and no symbols, got %+v", c)
	}
	// The local model's stated defaults match the code.
	model := cfg.Complete.Model
	if !model.ModelEnabled() || dev.Complete.Model.Declined {
		t.Errorf("shipped [complete.model] = %+v, want enabled and not declined", model)
	}
	if model.CommandOrDefault() != "llama-server" {
		t.Errorf("shipped [complete.model] command = %q", model.CommandOrDefault())
	}
	if model.IdleOrDefault() != DefaultLocalIdle ||
		model.StartupOrDefault() != DefaultLocalStartup ||
		model.TimeoutOrDefault() != DefaultLocalTimeout {
		t.Errorf("shipped [complete.model] durations = %v/%v/%v, want %v/%v/%v",
			model.IdleOrDefault(), model.StartupOrDefault(), model.TimeoutOrDefault(),
			DefaultLocalIdle, DefaultLocalStartup, DefaultLocalTimeout)
	}

	// The notification section documents its own defaults too.
	n := dev.Notifications
	if n.TitleTemplate() != DefaultTitleTemplate || n.BodyTemplate() != DefaultBodyTemplate {
		t.Errorf("shipped templates = %q/%q, want the defaults", n.TitleTemplate(), n.BodyTemplate())
	}
	if n.Enabled {
		t.Error("notifications should ship off by default")
	}
	// Auto-copy ships off, with no command and no places.
	if cfg.Clipboard.AutoCopy {
		t.Error("auto-copy should ship off: a clipboard that rewrites itself is remotely triggerable")
	}
	if dev.Clipboard.Command != "" {
		t.Errorf("shipped [clipboard] command = %q, want empty", dev.Clipboard.Command)
	}
	// Sound ships silent.
	if cfg.Notifications.Sound != "" {
		t.Errorf("shipped [notifications] sound = %q, want silent", cfg.Notifications.Sound)
	}
	if got := n.PopupTimeout(); got != DefaultTimeoutSeconds*time.Second {
		t.Errorf("shipped timeout = %v, want the documented default", got)
	}
	// Spelling ships on, hunspell, no dictionaries named (meaning every one installed).
	s := cfg.Spell
	if !s.SpellEnabled() {
		t.Error("spellcheck should ship on: it costs nothing where there is nothing to check with")
	}
	if got := s.EngineOrDefault(); got != "hunspell" {
		t.Errorf("shipped [spell] command = %q, want hunspell", got)
	}
	if len(s.Dictionaries) != 0 {
		t.Errorf("shipped [spell] dictionaries = %v, want empty (meaning all of them)", s.Dictionaries)
	}
	// The shipped underline equals the vocabulary's first entry, the omitted-key default.
	if dev.Spell.Underline != SpellCurly {
		t.Errorf("shipped [spell] underline = %q, want %q", dev.Spell.Underline, SpellCurly)
	}
	if want := SpellUnderlines()[0]; want != SpellCurly {
		t.Errorf("an omitted [spell] underline resolves to %q, but the file ships %q", want, SpellCurly)
	}
}
