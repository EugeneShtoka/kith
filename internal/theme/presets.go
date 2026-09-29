package theme

import (
	"charm.land/lipgloss/v2"

	"github.com/EugeneShtoka/kith/internal/themespec"
)

// Default is the default preset's palette.
func Default() Palette { return paletteOf(themespec.Default()) }

// Resolve builds a palette from a preset name ("" = default) and per-role hex
// overrides ("" = unchanged); themespec validates them.
func Resolve(preset string, overrides map[string]string) (Palette, error) {
	spec, err := themespec.Resolve(preset, overrides)
	if err != nil {
		return Palette{}, err //nolint:wrapcheck // themespec's message already names the setting
	}
	return paletteOf(spec), nil
}

// paletteOf turns color strings into colors: lipgloss reads "#rrggbb" as RGB and an
// ANSI slot ("12") as the terminal's own color.
func paletteOf(c themespec.Colors) Palette {
	return Palette{
		Accent: lipgloss.Color(c.Accent), Border: lipgloss.Color(c.Border), Text: lipgloss.Color(c.Text),
		Muted: lipgloss.Color(c.Muted), Cursor: lipgloss.Color(c.Cursor), Badge: lipgloss.Color(c.Badge),
		BadgeAlert: lipgloss.Color(c.BadgeAlert), SelectedBG: lipgloss.Color(c.SelectedBG),
		SelectedDimBG: lipgloss.Color(c.SelectedDimBG),
	}
}
