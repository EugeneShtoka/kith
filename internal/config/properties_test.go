package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Every property has documentation in default.toml for the settings screen.
func TestEveryPropertyIsDocumented(t *testing.T) {
	t.Parallel()
	props := Properties()
	if len(props) < 150 {
		t.Fatalf("%d properties; the walk lost most of the config", len(props))
	}
	for _, p := range props {
		if strings.TrimSpace(p.Doc) == "" {
			t.Errorf("%s has no comment in default.toml", p.Path)
		}
		for _, skip := range notProperties {
			if strings.HasPrefix(p.Path, skip+".") || p.Path == skip {
				t.Errorf("%s is listed, but %s is not a place for properties", p.Path, skip)
			}
		}
	}
}

// Each key gets its own comment, its group's, or its table-header entry; commented
// examples are not keys.
func TestDocsAreReadFromWhereTheyAre(t *testing.T) {
	t.Parallel()
	by := map[string]Property{}
	for _, p := range Properties() {
		by[p.Path] = p
	}
	for path, want := range map[string]string{
		"display.fps":             "repainted",             // its own comment
		"display.media.mode":      `"placeholder" (a text`, // the header's entry for it
		"display.media.max_width": "columns it may take",   // listed in the header, never written
		"display.send_receipts":   "Read receipts",         // prose starting "send_receipts = false…" is no key
		"agent.write.send":        "narrowed by reading",   // not the example line "send = [...]"
	} {
		if !strings.Contains(by[path].Doc, want) {
			t.Errorf("%s doc = %q, want it to hold %q", path, by[path].Doc, want)
		}
	}
	if d := by["notifications.title"].Default; d != `"{space} · {room} · {sender}"` {
		t.Errorf("title default = %s: a commented alternative replaced the default", d)
	}
}

// Each kind round-trips; empty unsets or zeroes; a wrong-kind value is refused; writes
// never reach a clone's source.
func TestPropertiesAreSetAndReadBack(t *testing.T) {
	t.Parallel()
	var base Settings
	on := true
	base.Device.Display.Mouse = &on
	cfg := base.Clone()
	for _, tc := range []struct{ path, text, want string }{
		{"display.color_messages", "true", "true"},
		{"display.mouse", "false", "false"},
		{"display.fps", "60", "60"},
		{"display.read_delay", "15", "15"},
		{"display.media.audio.speed", "1.5", "1.5"},
		{"tui.theme.accent", "#ff8800", "#ff8800"},
		{"display.priority", "tag:Work, Friends", "tag:Work, Friends"},
	} {
		if err := cfg.SetValue(tc.path, tc.text); err != nil {
			t.Fatalf("SetValue(%s, %q) = %v", tc.path, tc.text, err)
		}
		if got, set, ok := cfg.Value(tc.path); !ok || !set || got != tc.want {
			t.Errorf("%s = %q (set %v, ok %v), want %q", tc.path, got, set, ok, tc.want)
		}
	}
	if *base.Device.Display.Mouse != true {
		t.Error("setting the clone's display.mouse wrote through to the original")
	}
	if !slices.Equal(cfg.List("display.priority"), []string{"tag:Work", "Friends"}) {
		t.Errorf("list = %v", cfg.List("display.priority"))
	}
	for _, path := range []string{"display.mouse", "display.fps", "display.priority"} {
		if err := cfg.SetValue(path, ""); err != nil {
			t.Fatal(err)
		}
		if _, set, _ := cfg.Value(path); set {
			t.Errorf("%s is still set after an empty value", path)
		}
	}
	for path, bad := range map[string]string{"display.fps": "fast", "display.mouse": "maybe", "display.media.audio.speed": "fast"} {
		if err := cfg.SetValue(path, bad); err == nil {
			t.Errorf("%s took %q", path, bad)
		}
	}
	if err := cfg.SetValue("keys.quit", "x"); err == nil {
		t.Error("a key binding was set as a property")
	}
}

// Every bridge property is written and read back as a typed value of its kind, a clone's
// source untouched; nil, empty text and an empty list unset it; a value of another kind
// is refused; and nothing that is no bridge property is reached: a table, a list's
// element, a record table's field, a device property, a key binding.
func TestBridgePropertiesAreTypedValues(t *testing.T) {
	t.Parallel()
	sample := map[PropertyKind]any{
		PropertyBool: true, PropertyOptionalBool: false, PropertyInt: 7, PropertyOptionalInt: 0,
		PropertyFloat: 0.5, PropertyOptionalFloat: 1.5, PropertyText: "x", PropertyList: []string{"a", "b"},
	}
	wrong := map[PropertyKind]any{
		PropertyBool: "true", PropertyOptionalBool: 1, PropertyInt: "7", PropertyOptionalInt: true,
		PropertyFloat: 1, PropertyOptionalFloat: "1.5", PropertyText: []string{"x"}, PropertyList: "a, b",
	}
	var base Config
	for _, p := range Properties() {
		if p.File != BridgeFile {
			continue
		}
		cfg := base.Clone()
		if err := cfg.SetProperty(p.Path, sample[p.Kind]); err != nil {
			t.Fatalf("SetProperty(%s, %v) = %v", p.Path, sample[p.Kind], err)
		}
		got, set, ok := cfg.PropertyValue(p.Path)
		if !ok || !reflect.DeepEqual(got, sample[p.Kind]) {
			t.Errorf("%s = %#v (ok %v), want %#v", p.Path, got, ok, sample[p.Kind])
		}
		optional := p.Kind == PropertyOptionalBool || p.Kind == PropertyOptionalInt || p.Kind == PropertyOptionalFloat
		if !set && optional {
			t.Errorf("%s reads unset after a value was written", p.Path)
		}
		if _, set, _ := base.PropertyValue(p.Path); set {
			t.Errorf("writing %s into a clone wrote through to its source", p.Path)
		}
		if p.Kind == PropertyList {
			mine := []string{"c"}
			if err := cfg.SetProperty(p.Path, mine); err != nil {
				t.Fatal(err)
			}
			mine[0] = "changed by the caller later"
			if got, _, _ := cfg.PropertyValue(p.Path); !reflect.DeepEqual(got, []string{"c"}) {
				t.Errorf("%s shares the list it was given: %v", p.Path, got)
			}
		}
		if err := cfg.SetProperty(p.Path, wrong[p.Kind]); err == nil {
			t.Errorf("%s took %#v", p.Path, wrong[p.Kind])
		}
		if err := cfg.SetProperty(p.Path, nil); err != nil {
			t.Fatal(err)
		}
		if _, set, _ := cfg.PropertyValue(p.Path); set {
			t.Errorf("%s is still set after nil", p.Path)
		}
		if d, ok := p.DefaultValue(); ok && reflect.TypeOf(d) != reflect.TypeOf(sample[p.Kind]) {
			t.Errorf("%s's default %#v is not a %T", p.Path, d, sample[p.Kind])
		}
	}
	var cfg Config
	for path, values := range map[string][2]any{
		"display.time_format":   {"12h", ""},
		"display.tracked.words": {[]string{"x"}, []string{}},
	} {
		if err := cfg.SetProperty(path, values[0]); err != nil {
			t.Fatal(err)
		}
		if err := cfg.SetProperty(path, values[1]); err != nil {
			t.Fatal(err)
		}
		if _, set, _ := cfg.PropertyValue(path); set {
			t.Errorf("%s is still set after %#v", path, values[1])
		}
	}
	for _, path := range []string{"", "display", "display.tracked", "notifications.rule", "notifications.rule.name",
		"display.tracked.words.0", "profile", "display.skin_tone", "keys.quit", "storage.data_dir", "gui.theme.mode"} {
		if _, _, ok := cfg.PropertyValue(path); ok {
			t.Errorf("%q reads as a bridge property", path)
		}
		if err := cfg.SetProperty(path, "x"); err == nil {
			t.Errorf("%q was written as a bridge property", path)
		}
	}
}

// A default is typed by its property's kind; one written only as an example is none.
func TestDefaultsAreTyped(t *testing.T) {
	t.Parallel()
	by := map[string]Property{}
	for _, p := range Properties() {
		if p.File == BridgeFile {
			by[p.Path] = p
		}
	}
	for path, want := range map[string]any{
		"display.time_format": "24h", "codes.min_length": 4, "spell.enabled": true,
		"assist.rewrite.temperature": 0.0, "assist.thread_name.stop": []string{"\n"},
	} {
		if got, ok := by[path].DefaultValue(); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s's default = %#v (%v), want %#v", path, got, ok, want)
		}
	}
	if got, ok := by["display.priority"].DefaultValue(); ok {
		t.Errorf("display.priority's example reads as its default: %#v", got)
	}
}
