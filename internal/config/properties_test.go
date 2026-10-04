package config

import (
	"slices"
	"strings"
	"testing"
)

// Every property is documented in default.toml: the settings screen shows that text,
// and a key added without it would show nothing.
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

// default.toml's text reaches the right key: its own comment, its group's, or its
// line in the table header's list; and an example in the comments is no key.
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

// Each kind is written and read back; empty unsets an optional and zeroes the rest; a
// value that is not of the kind is refused; and a write never reaches the config the
// edited one was cloned from.
func TestPropertiesAreSetAndReadBack(t *testing.T) {
	t.Parallel()
	var base Config
	on := true
	base.Display.Mouse = &on
	cfg := base.Clone()
	for _, tc := range []struct{ path, text, want string }{
		{"display.color_messages", "true", "true"},
		{"display.mouse", "false", "false"},
		{"display.fps", "60", "60"},
		{"display.read_delay", "15", "15"},
		{"spam.ratio", "0.25", "0.25"},
		{"display.theme.accent", "#ff8800", "#ff8800"},
		{"display.priority", "tag:Work, Friends", "tag:Work, Friends"},
	} {
		if err := cfg.SetValue(tc.path, tc.text); err != nil {
			t.Fatalf("SetValue(%s, %q) = %v", tc.path, tc.text, err)
		}
		if got, set, ok := cfg.Value(tc.path); !ok || !set || got != tc.want {
			t.Errorf("%s = %q (set %v, ok %v), want %q", tc.path, got, set, ok, tc.want)
		}
	}
	if *base.Display.Mouse != true {
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
	for path, bad := range map[string]string{"display.fps": "fast", "display.mouse": "maybe", "spam.ratio": "half"} {
		if err := cfg.SetValue(path, bad); err == nil {
			t.Errorf("%s took %q", path, bad)
		}
	}
	if err := cfg.SetValue("keys.quit", "x"); err == nil {
		t.Error("a key binding was set as a property")
	}
}
