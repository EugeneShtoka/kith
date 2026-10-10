// Package themespec is the theme config as data: the built-in presets and the
// per-role overrides, validated, as the color strings the config writes ("#00aaff",
// or an ANSI slot like "12"). It imports nothing, so the headless binaries can check a
// config's [display.theme] without linking a terminal renderer; internal/theme turns
// the strings into colors for the TUI.
package themespec

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Colors is the nine roles a theme is made of, as color strings (see Palette in
// internal/theme for what each role colors).
type Colors struct {
	Accent        string
	Border        string
	Text          string
	Muted         string
	Cursor        string
	Badge         string
	BadgeAlert    string
	SelectedBG    string
	SelectedDimBG string
}

// presets are the built-in palettes by config name. "terminal" uses ANSI slots 0–15,
// so the client follows whatever scheme the terminal already has.
var presets = map[string]func() Colors{
	"cobalt2":  cobalt2,
	"gruvbox":  gruvbox,
	"nord":     nord,
	"terminal": terminal,
}

const defaultPreset = "cobalt2"

// Default is the default preset.
func Default() Colors { return cobalt2() }

// cobalt2 matches the author's WezTerm scheme and cobalt2.nvim.
func cobalt2() Colors {
	return Colors{
		Accent:        "#00aaff",
		Border:        "#0050a4",
		Text:          "#8ff586",
		Muted:         "#668799",
		Cursor:        "#ffc600",
		Badge:         "#ffc600",
		BadgeAlert:    "#ff628c",
		SelectedBG:    "#185294",
		SelectedDimBG: "#0d3a58",
	}
}

func gruvbox() Colors {
	return Colors{
		Accent:        "#83a598", // bright_blue
		Border:        "#665c54", // bg3
		Text:          "#ebdbb2", // fg1
		Muted:         "#928374", // gray
		Cursor:        "#fabd2f", // bright_yellow
		Badge:         "#fabd2f", // bright_yellow
		BadgeAlert:    "#fb4934", // bright_red
		SelectedBG:    "#504945", // bg2
		SelectedDimBG: "#3c3836", // bg1
	}
}

func nord() Colors {
	return Colors{
		Accent:        "#88c0d0", // nord8
		Border:        "#4c566a", // nord3
		Text:          "#d8dee9", // nord4
		Muted:         "#7b88a1", // nord3 brightened
		Cursor:        "#ebcb8b", // nord13
		Badge:         "#ebcb8b", // nord13
		BadgeAlert:    "#bf616a", // nord11
		SelectedBG:    "#434c5e", // nord2
		SelectedDimBG: "#3b4252", // nord1
	}
}

func terminal() Colors {
	return Colors{
		Accent:        "12", // bright blue
		Border:        "8",  // bright black
		Text:          "7",  // white
		Muted:         "8",  // bright black
		Cursor:        "11", // bright yellow
		Badge:         "11", // bright yellow
		BadgeAlert:    "9",  // bright red
		SelectedBG:    "8",  // bright black
		SelectedDimBG: "0",  // black
	}
}

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// Resolve is a preset by name ("" = default) with per-role hex overrides ("" =
// unchanged). Unknown presets, roles or malformed colors are errors, not silent
// fallbacks.
func Resolve(preset string, overrides map[string]string) (Colors, error) {
	name := strings.ToLower(strings.TrimSpace(preset))
	if name == "" {
		name = defaultPreset
	}
	mk, ok := presets[name]
	if !ok {
		return Colors{}, fmt.Errorf("theme: unknown preset %q (have %s)", preset, strings.Join(Names(), ", "))
	}
	base := mk()
	for role, value := range overrides {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !hexColor.MatchString(value) {
			return Colors{}, fmt.Errorf("theme: %s = %q is not a color (want #rgb or #rrggbb)", role, value)
		}
		field, ok := roleOf(&base, role)
		if !ok {
			return Colors{}, fmt.Errorf("theme: unknown color %q", role)
		}
		*field = value
	}
	return base, nil
}

// Names is the built-in presets' names, sorted.
func Names() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// roleOf maps a TOML role name to its field.
func roleOf(c *Colors, role string) (*string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "accent":
		return &c.Accent, true
	case "border":
		return &c.Border, true
	case "text":
		return &c.Text, true
	case "muted":
		return &c.Muted, true
	case "cursor":
		return &c.Cursor, true
	case "badge":
		return &c.Badge, true
	case "badge_alert":
		return &c.BadgeAlert, true
	case "selected_bg":
		return &c.SelectedBG, true
	case "selected_dim_bg":
		return &c.SelectedDimBG, true
	}
	return nil, false
}

// namedColors maps friendly config names to hex.
var namedColors = map[string]string{
	"red":        "#ff628c", // dark_pink: cobalt2's readable red, not its error #FF0000
	"green":      "#3ad900",
	"teal":       "#1de9b6", // WhatsApp's teal, apart from the green cobalt2 writes text in
	"orchid":     "#e67cff", // Meta's pink-violet
	"periwinkle": "#9ab8ff", // a light blue, apart from the accent's
	"coral":      "#ff8a5c", // apart from the alert's pink and the badge's yellow
	"yellow":     "#ffc600",
	"blue":       "#00aaff",
	"magenta":    "#967efb", // purple
	"cyan":       "#80fcff", // light_blue
	"orange":     "#ff9d00", // light_orange
	"white":      "#deebfe", // light_purple: cobalt2's near-white
}

// Color is a color a config writes, as "#rrggbb": that value, or one of namedColors
// by name (any case). ok is false for an empty or unrecognized value.
func Color(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if hex, ok := namedColors[strings.ToLower(s)]; ok {
		return hex, true
	}
	if len(s) == 7 && s[0] == '#' && isHex(s[1:]) {
		return s, true
	}
	return "", false
}

// ColorNames is the names Color knows, sorted.
func ColorNames() []string {
	names := make([]string, 0, len(namedColors))
	for name := range namedColors {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
