package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The starter config we write on first run must parse, and its documented values must
// *be* the built-in defaults.
func TestShippedDefaultTOMLMatchesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := WriteDefaultIfMissing(path); err != nil {
		t.Fatalf("WriteDefaultIfMissing: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The shipped file leaves the two required fields blank for the user to fill.
	filled := strings.NewReplacer(
		`homeserver = ""`, `homeserver = "https://example.org"`,
		`user       = ""`, `user       = "@me:example.org"`,
	).Replace(string(body))
	if werr := os.WriteFile(path, []byte(filled), 0o600); werr != nil {
		t.Fatalf("write: %v", werr)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped default config does not load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Keys, DefaultKeys()) {
		t.Errorf("shipped [keys] differs from DefaultKeys()\n got %+v\nwant %+v", cfg.Keys, DefaultKeys())
	}
	// So does the code-detection section: the annotated file states the shape, and a
	// stated default that has drifted from the code is worse than none.
	c := cfg.Codes
	if c.Min() != 4 || c.Max() != 8 || !c.LettersAllowed() || !c.DigitsAllowed() || !c.DigitRequired() {
		t.Errorf("shipped [codes] = %+v, want 4-8 alnum with a digit required", c)
	}
	if c.Symbols != "" || len(c.Include) != 0 || len(c.Exclude) != 0 {
		t.Errorf("shipped [codes] should ship empty lists and no symbols, got %+v", c)
	}
	// The local model's section states three numbers the feature rests on, and a stated
	// default that has drifted from the code is worse than none.
	model := cfg.Complete.Model
	if !model.ModelEnabled() || model.Declined {
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
	n := cfg.Notifications
	if n.TitleTemplate() != DefaultTitleTemplate || n.BodyTemplate() != DefaultBodyTemplate {
		t.Errorf("shipped templates = %q/%q, want the defaults", n.TitleTemplate(), n.BodyTemplate())
	}
	if n.Enabled {
		t.Error("notifications should ship off by default")
	}
	// Auto-copy ships off, with nothing to copy through and nowhere to copy from —
	// three separate things that would each have to be turned on deliberately before a
	// message could rewrite the clipboard.
	if cfg.Clipboard.AutoCopy {
		t.Error("auto-copy should ship off: a clipboard that rewrites itself is remotely triggerable")
	}
	if cfg.Clipboard.Command != "" {
		t.Errorf("shipped [clipboard] command = %q, want empty", cfg.Clipboard.Command)
	}
	// Sound ships silent — the setting exists, and it is off until asked for.
	if n.Sound != "" {
		t.Errorf("shipped [notifications] sound = %q, want silent", n.Sound)
	}
	if got := n.PopupTimeout(); got != DefaultTimeoutSeconds*time.Second {
		t.Errorf("shipped timeout = %v, want the documented default", got)
	}
	// Spelling ships on, on hunspell, with no dictionary named — because naming none
	// means "every one installed", which is what lets a dictionary added later work
	// without also editing this file.
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
	// The shipped underline is the one the documentation describes, and the first entry
	// of the vocabulary is what an omitted key resolves to — so these have to be the
	// same value or the file is documenting a default it does not produce.
	if s.Underline != SpellCurly {
		t.Errorf("shipped [spell] underline = %q, want %q", s.Underline, SpellCurly)
	}
	if want := SpellUnderlines()[0]; want != SpellCurly {
		t.Errorf("an omitted [spell] underline resolves to %q, but the file ships %q", want, SpellCurly)
	}
}
