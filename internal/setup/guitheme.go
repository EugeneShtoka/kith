package setup

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
)

// themeColor is a color as [gui.theme] takes it: "#rrggbb", which the window's color picker
// writes and reads.
var themeColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// GUITheme refuses a [gui.theme] the desktop could not draw: a mode it does not know, or a
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
		if !slices.Contains(config.GUIThemeModes(), value) {
			return fmt.Errorf("gui.theme.mode: unknown mode %q (want %s)", value, strings.Join(config.GUIThemeModes(), ", "))
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
