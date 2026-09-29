package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FillDefaults is the non-obvious correctness bit.
func TestFillDefaults(t *testing.T) {
	t.Parallel()

	// The empty case: a config with no [keys] section is exactly the defaults.
	var empty Keys
	empty.FillDefaults()
	if !reflect.DeepEqual(empty, DefaultKeys()) {
		t.Errorf("an empty Keys should fill to the defaults, got %+v", empty)
	}

	// The partial case: one field set, everything else — including fields in nested
	// tables the file never mentioned — filled in.
	partial := Keys{Quit: "Q", Timeline: TimelineKeys{React: "e"}}
	partial.FillDefaults()
	if partial.Quit != "Q" || partial.Timeline.React != "e" {
		t.Errorf("FillDefaults overwrote a set value: %+v", partial)
	}
	if partial.Help != DefaultKeys().Help {
		t.Errorf("Help = %q, want the default %q", partial.Help, DefaultKeys().Help)
	}
	if partial.Timeline.Reply != DefaultKeys().Timeline.Reply {
		t.Errorf("Timeline.Reply = %q, want the default", partial.Timeline.Reply)
	}
	if partial.Nav.Up != DefaultKeys().Nav.Up {
		t.Errorf("Nav.Up = %q, want the default — a whole omitted table must fill", partial.Nav.Up)
	}
}

// Every default binding is non-empty, or an action ships with no key.
func TestDefaultKeysAreComplete(t *testing.T) {
	t.Parallel()

	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		for i := range v.NumField() {
			field := v.Type().Field(i)
			name := strings.TrimPrefix(path+"."+field.Name, ".")
			switch v.Field(i).Kind() {
			case reflect.String:
				if v.Field(i).String() == "" {
					t.Errorf("%s has no default binding", name)
				}
			case reflect.Struct:
				walk(v.Field(i), name)
			}
		}
	}
	walk(reflect.ValueOf(DefaultKeys()), "")
}

func TestLoadKeys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
homeserver = "https://example.org"
user = "@me:example.org"

[keys]
quit = "Q,ctrl+q"

[keys.timeline]
react = "e"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Keys.Quit != "Q,ctrl+q" {
		t.Errorf("Quit = %q, want the configured list", cfg.Keys.Quit)
	}
	if cfg.Keys.Timeline.React != "e" {
		t.Errorf("Timeline.React = %q, want e", cfg.Keys.Timeline.React)
	}
	// Load fills the rest, so nothing the file omitted is left unbound.
	if cfg.Keys.Help != DefaultKeys().Help {
		t.Errorf("Help = %q, want the default — Load must fill defaults", cfg.Keys.Help)
	}
	if cfg.Keys.Nav.Up != DefaultKeys().Nav.Up {
		t.Errorf("Nav.Up = %q, want the default", cfg.Keys.Nav.Up)
	}
}

// A config with no [keys] section at all still ends up fully bound.
func TestLoadWithoutKeysSection(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	body := "homeserver = \"https://example.org\"\nuser = \"@me:example.org\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Keys, DefaultKeys()) {
		t.Errorf("Keys = %+v, want the defaults", cfg.Keys)
	}
}

// [[keys.jump]] blocks decode through the TOML tags; a wrong tag is a feature that
// silently does nothing.
func TestLoadJumpBindings(t *testing.T) {
	t.Parallel()

	cfg, err := loadText(t, `
[keys]
jump_to = "ctrl+j"

[[keys.jump]]
chord = "g w"
target = "space:Work"

[[keys.jump]]
chord = "g i"
target = "room:!inbox:example.org"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Jumps{{Chord: "g w", Target: "space:Work"}, {Chord: "g i", Target: "room:!inbox:example.org"}}
	if cfg.Keys.JumpTo != "ctrl+j" || !reflect.DeepEqual(cfg.Keys.Jump, want) {
		t.Errorf("JumpTo = %q, Jump = %+v", cfg.Keys.JumpTo, cfg.Keys.Jump)
	}
}

// No [keys.jump] means no jump bindings — there is nothing to default to, since the
// places are the user's own.
func TestJumpHasNoDefaults(t *testing.T) {
	t.Parallel()

	if len(DefaultKeys().Jump) != 0 {
		t.Errorf("DefaultKeys ships jump bindings: %+v", DefaultKeys().Jump)
	}
	keys := Keys{Jump: Jumps{{Chord: "g w", Target: "space:Work"}}}
	keys.FillDefaults()
	if len(keys.Jump) != 1 || keys.Jump.Map()["g w"] != "space:Work" {
		t.Errorf("FillDefaults disturbed the jump table: %+v", keys.Jump)
	}
}

// A chord bound twice is the one thing the map spelling made impossible by
// construction, so the list has to say so — and saying so beats what a map did, which
// was silently keep whichever binding was parsed last.
func TestAChordGoesToOnePlace(t *testing.T) {
	t.Parallel()

	ok := Jumps{{Chord: "g w", Target: "space:Work"}, {Chord: "g d", Target: "dm:Dana"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("two distinct chords were refused: %v", err)
	}
	dup := Jumps{{Chord: "g w", Target: "space:Work"}, {Chord: "g w", Target: "dm:Dana"}}
	err := dup.Validate()
	if err == nil {
		t.Fatal("a chord bound twice was accepted")
	}
	// The error names the chord, which is the whole advantage over the map.
	if !strings.Contains(err.Error(), `"g w"`) {
		t.Errorf("error = %v, want it to name the doubled chord", err)
	}
	if err := (Jumps{{Target: "space:Work"}}).Validate(); err == nil {
		t.Error("a binding with no chord was accepted")
	}
}
