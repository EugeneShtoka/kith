package setup

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// themeColor is a color as [gui.theme] takes it: "#rrggbb", which the window's color picker
// writes and reads.
var themeColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// themeName is a theme file's name as mode names it: themes/<name>.toml.
var themeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// GUIThemeFiles refuses a theme file the desktop could not draw: a base that is not light
// or dark, a name mode could not name, or a color that is not "#rrggbb".
func GUIThemeFiles(themes map[string]config.GUIThemeFile) error {
	for _, name := range slices.Sorted(maps.Keys(themes)) {
		t := themes[name]
		if !themeName.MatchString(name) || name == "system" {
			return fmt.Errorf("theme %q: a theme's file is named in lower case letters, digits, - and _, and is not system", name)
		}
		if t.Base != "light" && t.Base != "dark" || (name == "light" || name == "dark") && t.Base != name {
			return fmt.Errorf("theme %s: base %q (want light or dark; the light and dark themes' own)", name, t.Base)
		}
		for _, role := range config.GUIThemeRoles() {
			if c := t.Role(role); c != "" && !themeColor.MatchString(c) {
				return fmt.Errorf("theme %s: %s %q is no color (write #rrggbb)", name, role, c)
			}
		}
	}
	return nil
}

// GUITheme refuses a [gui.theme] the desktop could not draw: a mode or faces it does not know, or a
// color that is not "#rrggbb". The first in its keys' order is named.
func GUITheme(l config.GUITheme) error {
	for _, key := range l.Keys() {
		value, _ := l.Get(key)
		if err := GUIThemeValue(key, value); err != nil {
			return err
		}
	}
	return nil
}

// GUIThemeValue refuses a value the key at path in [gui.theme] does not take; empty, which
// unsets it, is always taken.
func GUIThemeValue(path, value string) error {
	switch {
	case value == "":
		return nil
	case path == "mode":
		if !slices.Contains(config.GUIThemeModes(), value) && !themeName.MatchString(value) {
			return fmt.Errorf("gui.theme.mode: unknown mode %q (want %s, or a theme file's name)", value, strings.Join(config.GUIThemeModes(), ", "))
		}
	case path == "faces":
		if !slices.Contains(config.GUIThemeFaces(), value) {
			return fmt.Errorf("gui.theme.faces: unknown way %q (want %s)", value, strings.Join(config.GUIThemeFaces(), ", "))
		}
	case !themeColor.MatchString(value):
		return fmt.Errorf("gui.theme.%s: %q is no color (write #rrggbb)", path, value)
	}
	return nil
}

// SenderKey is the key a room's color slots remember everyone in the i-th
// [[display.identity]] by, one sender for them all: its alias, else its first ID.
func SenderKey(i int, id config.Identity) string {
	key := id.Alias
	if key == "" && len(id.IDs) > 0 {
		key = id.IDs[0]
	}
	if key == "" {
		key = fmt.Sprintf("identity-%d", i)
	}
	return "id:" + key
}
