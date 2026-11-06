package setup

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// A [gui.theme] is refused for a mode the window does not know or a color that is not
// #rrggbb, naming the key; an unset key and every #rrggbb, in any case, are taken.
func TestAGUIThemeIsRefusedForWhatTheWindowCannotDraw(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key, value, refused string
	}{
		{"mode", "dark", ""},
		{"mode", "system", ""},
		{"mode", "cobalt", ""}, // a theme file's name
		{"mode", "Sepia Tone", "gui.theme.mode"},
		{"faces", "initials", ""},
		{"faces", "pictures", ""},
		{"faces", "photos", "gui.theme.faces"},
		{"light.accent", "#1F6feb", ""},
		{"dark.text", "#fff", "gui.theme.dark.text"},
		{"dark.text", "white", "gui.theme.dark.text"},
		{"people.matrix:@a:x", "#1f6feb ", "gui.theme.people.matrix:@a:x"},
		{"accounts.whatsapp:main/", "#00ff00", ""},
		{"accounts.whatsapp:main/", "", ""},
	} {
		var l config.GUITheme
		if !l.Set(tc.key, tc.value) {
			t.Fatalf("no key %s", tc.key)
		}
		err := GUITheme(l)
		if (err != nil) != (tc.refused != "") || (err != nil && !strings.Contains(err.Error(), tc.refused+":")) {
			t.Errorf("%s = %q: %v, want refused naming %q", tc.key, tc.value, err, tc.refused)
		}
		cfg := config.Config{GUI: config.GUI{Theme: l}, Profiles: []config.Profile{{Name: "Home", Include: []string{"*"}}}}
		if err := Validate(cfg); (err != nil) != (tc.refused != "") {
			t.Errorf("the bridge's config with %s = %q: %v", tc.key, tc.value, err)
		}
		if err := ValidateDevice(config.Device{GUI: config.GUI{Theme: l}}); (err != nil) != (tc.refused != "") {
			t.Errorf("a device file with %s = %q: %v", tc.key, tc.value, err)
		}
	}
}

// Everyone in an identity is one sender to a room's color slots, keyed as the TUI keys
// them: its alias, else its first ID, else its place in the list.
func TestSenderKeysAreTheTUIs(t *testing.T) {
	t.Parallel()
	for i, tc := range []struct {
		id   config.Identity
		want string
	}{
		{config.Identity{Alias: "Ada", IDs: []string{"matrix:@a:x"}}, "id:Ada"},
		{config.Identity{IDs: []string{"matrix:@b:x", "whatsapp:1@s.whatsapp.net"}}, "id:matrix:@b:x"},
		{config.Identity{}, "id:identity-2"},
	} {
		if got := SenderKey(i, tc.id); got != tc.want {
			t.Errorf("SenderKey(%d, %+v) = %q, want %q", i, tc.id, got, tc.want)
		}
	}
}

// A theme file is refused for a base that is neither light nor dark, a name mode could
// not name, or a color that is not #rrggbb.
func TestAThemeFileIsRefusedForWhatTheWindowCannotDraw(t *testing.T) {
	t.Parallel()
	good := config.GUIThemeFile{Base: "dark", GUIRoles: config.GUIRoles{Accent: "#00aaff"}}
	if err := GUIThemeFiles(map[string]config.GUIThemeFile{"cobalt": good}); err != nil {
		t.Errorf("a good theme refused: %v", err)
	}
	for name, themes := range map[string]map[string]config.GUIThemeFile{
		"base":  {"dim": {Base: "dim"}},
		"name":  {"My Theme": good},
		"mode":  {"system": good},
		"light": {"light": good}, // the light theme's base is light
		"color": {"bad": {Base: "light", GUIRoles: config.GUIRoles{Text: "white"}}},
	} {
		if err := GUIThemeFiles(themes); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}
