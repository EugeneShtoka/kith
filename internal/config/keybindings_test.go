package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFillDefaults(t *testing.T) {
	t.Parallel()

	// No [keys] section: exactly the defaults.
	var empty Keys
	empty.FillDefaults()
	if !reflect.DeepEqual(empty, DefaultKeys()) {
		t.Errorf("an empty Keys should fill to the defaults, got %+v", empty)
	}

	// One field set: everything else, nested tables included, is filled.
	partial := Keys{Bindings: map[string]string{"quit": "Q", "timeline.react": "e"}}
	partial.FillDefaults()
	if partial.Bindings["quit"] != "Q" || partial.Bindings["timeline.react"] != "e" {
		t.Errorf("FillDefaults overwrote a set value: %+v", partial)
	}
	if partial.Bindings["help"] != DefaultKeys().Bindings["help"] {
		t.Errorf("Help = %q, want the default %q", partial.Bindings["help"], DefaultKeys().Bindings["help"])
	}
	if partial.Bindings["timeline.reply"] != DefaultKeys().Bindings["timeline.reply"] {
		t.Errorf("Timeline.Reply = %q, want the default", partial.Bindings["timeline.reply"])
	}
	if partial.Bindings["nav.up"] != DefaultKeys().Bindings["nav.up"] {
		t.Errorf("Nav.Up = %q, want the default — a whole omitted table must fill", partial.Bindings["nav.up"])
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
	if cfg.Keys.Bindings["quit"] != "Q,ctrl+q" {
		t.Errorf("Quit = %q, want the configured list", cfg.Keys.Bindings["quit"])
	}
	if cfg.Keys.Bindings["timeline.react"] != "e" {
		t.Errorf("Timeline.React = %q, want e", cfg.Keys.Bindings["timeline.react"])
	}
	// Load fills the rest, so nothing the file omitted is left unbound.
	if cfg.Keys.Bindings["help"] != DefaultKeys().Bindings["help"] {
		t.Errorf("Help = %q, want the default — Load must fill defaults", cfg.Keys.Bindings["help"])
	}
	if cfg.Keys.Bindings["nav.up"] != DefaultKeys().Bindings["nav.up"] {
		t.Errorf("Nav.Up = %q, want the default", cfg.Keys.Bindings["nav.up"])
	}
}

// A config with no [keys] section at all still ends up fully bound.
func TestLoadWithoutKeysSection(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	body := "allow_token_file = true\n"
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

// [[keys.jump]] blocks decode through the TOML tags.
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
	if cfg.Keys.Bindings["jump_to"] != "ctrl+j" || !reflect.DeepEqual(cfg.Keys.Jump, want) {
		t.Errorf("JumpTo = %q, Jump = %+v", cfg.Keys.Bindings["jump_to"], cfg.Keys.Jump)
	}
}

// No [keys.jump] means no jump bindings.
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

// A chord bound twice is refused.
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
	// The error names the chord.
	if !strings.Contains(err.Error(), `"g w"`) {
		t.Errorf("error = %v, want it to name the doubled chord", err)
	}
	if err := (Jumps{{Target: "space:Work"}}).Validate(); err == nil {
		t.Error("a binding with no chord was accepted")
	}
}

// Absent tag keys are the starter's (`!` into Spam); `tag = []` is none, and survives a
// save.
func TestTagKeysDefaultToTheStartersAndCanBeEmptied(t *testing.T) {
	t.Parallel()
	absent, err := Decode("")
	if err != nil {
		t.Fatal(err)
	}
	if len(absent.Keys.Tag) != 1 || absent.Keys.Tag[0] != (TagKey{Chord: "!", Tag: "Spam"}) {
		t.Errorf("absent = %+v, want the starter's", absent.Keys.Tag)
	}
	emptied, err := Decode("[keys]\ntag = []\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(emptied.Keys.Tag) != 0 {
		t.Errorf("tag = [] decoded as %+v, want none", emptied.Keys.Tag)
	}
	again, err := Decode(Encode(emptied))
	if err != nil || len(again.Keys.Tag) != 0 {
		t.Errorf("after a save: %+v, %v; want none still", again.Keys.Tag, err)
	}
}

// Every binding survives Save and Load under its own path, whichever table it is in.
func TestEveryBindingRoundTrips(t *testing.T) {
	t.Parallel()

	cfg, err := loadText(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for path := range keyByPath {
		cfg.Keys.Bindings[path] = "f12," + path // distinct from every default and each other
	}
	cfg.Keys.Jump = Jumps{{Chord: "g w", Target: "space:Work"}}
	cfg.Keys.Tag = TagKeys{}
	reloaded, err := Decode(Encode(cfg))
	if err != nil {
		t.Fatalf("reload: %v\n%s", err, Encode(cfg))
	}
	if !reflect.DeepEqual(reloaded.Keys, cfg.Keys) {
		t.Errorf("keys changed across a save:\n got %+v\nwant %+v", reloaded.Keys, cfg.Keys)
	}
}

// What [keys] cannot hold is refused, naming the key, as everywhere else in the file.
func TestLoadRefusesMalformedKeys(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		"[keys]\nquitt = \"q\"\n":                                      "keys.quitt",
		"[keys.timeline]\nreplay = \"r\"\n":                            "keys.timeline.replay",
		"[keys.timelime]\nreply = \"r\"\n":                             "keys.timelime.reply",
		"[keys]\ntimeline = \"r\"\n":                                   "keys.timeline",
		"[keys.timeline.reply]\nx = \"r\"\n":                           "keys.timeline.reply",
		"[keys]\nquit = 5\n":                                           "keys.quit",
		"[keys]\nquit = [\"q\"]\n":                                     "keys.quit",
		"[[keys.jump]]\nchord = \"g w\"\ntarget = \"x\"\nto = \"y\"\n": "keys.jump.to",
		"[[keys.tag]]\nchord = \"t s\"\ntag = 1\n":                     "keys.tag.tag",
		"[keys]\njump = \"g w\"\n":                                     "keys.jump",
	} {
		_, err := loadText(t, body)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one naming %s", body, err, want)
		}
	}
}
