// Package theme is kith's visual palette and the lipgloss styles built from it.
// It imports nothing internal.
package theme

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette holds the nine colors a theme is made of, one per role.
type Palette struct {
	// Accent: focused pane frame and title.
	Accent color.Color
	// Border: unfocused frame.
	Border color.Color
	Text   color.Color
	// Muted: hints, timestamps, dividers, empty states.
	Muted color.Color
	// Cursor: selection marker and composer prompt glyph.
	Cursor color.Color
	// Badge is an unread count; BadgeAlert one that highlights you (mention/keyword).
	Badge      color.Color
	BadgeAlert color.Color
	// SelectedBG fills the cursor row in the focused pane, SelectedDimBG in an unfocused one.
	SelectedBG    color.Color
	SelectedDimBG color.Color
}

// Theme bundles the ready-to-use styles the views apply. Border styles carry no
// fixed size — the caller sets Width/Height per frame from the terminal size.
type Theme struct {
	Palette Palette

	Pane        lipgloss.Style // inactive pane frame (rounded border)
	PaneActive  lipgloss.Style // focused pane frame (accent border)
	Title       lipgloss.Style // pane header text
	TitleActive lipgloss.Style // pane header when focused
	Muted       lipgloss.Style // hints / empty states / dividers
	Faint       lipgloss.Style // de-emphasis by terminal attribute rather than color
	Prompt      lipgloss.Style // composer "›" glyph
}

// New builds the styles from a palette.
func New(p Palette) Theme {
	border := lipgloss.RoundedBorder()
	pane := lipgloss.NewStyle().Border(border).BorderForeground(p.Border)

	return Theme{
		Palette:     p,
		Pane:        pane,
		PaneActive:  pane.BorderForeground(p.Accent),
		Title:       lipgloss.NewStyle().Foreground(p.Muted).Bold(true),
		TitleActive: lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		Muted:       lipgloss.NewStyle().Foreground(p.Muted),
		Faint:       lipgloss.NewStyle().Faint(true),
		Prompt:      lipgloss.NewStyle().Foreground(p.Cursor).Bold(true),
	}
}

// Row styles a selectable overlay row that does not span its pane: selection is a
// foreground change, since a fill that stops where the text does looks broken.
func (t Theme) Row(selected, focused bool) lipgloss.Style {
	switch {
	case selected && focused:
		return lipgloss.NewStyle().Foreground(t.Palette.Accent).Bold(true)
	case selected:
		return lipgloss.NewStyle().Foreground(t.Palette.Text).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(t.Palette.Text)
	}
}

// PaneRow styles a full-width rail/room-list row. Selection is a background fill,
// dimmer when unfocused, so the cursor stays findable in a pane without focus.
func (t Theme) PaneRow(selected, focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(t.Palette.Text)
	if selected {
		s = s.Bold(true)
	}
	return t.OnPaneRow(s, selected, focused)
}

// Marker styles the ▸ leading a row: the row's fill, in the cursor color.
func (t Theme) Marker(selected, focused bool) lipgloss.Style {
	style := t.PaneRow(selected, focused)
	if !selected {
		return style
	}
	if focused {
		return style.Foreground(t.Palette.Cursor)
	}
	return style.Foreground(t.Palette.Muted)
}

// OnPaneRow repaints s onto a row's background so e.g. a badge doesn't punch a hole in the fill.
func (t Theme) OnPaneRow(s lipgloss.Style, selected, focused bool) lipgloss.Style {
	switch {
	case selected && focused:
		return s.Background(t.Palette.SelectedBG)
	case selected:
		return s.Background(t.Palette.SelectedDimBG)
	default:
		return s
	}
}

// Badge styles an unread count (foreground only); highlight uses BadgeAlert.
func (t Theme) Badge(highlight bool) lipgloss.Style {
	c := t.Palette.Badge
	if highlight {
		c = t.Palette.BadgeAlert
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true)
}

// Golden-ratio hue steps keep consecutive sender colors maximally distinct.
const goldenRatioConjugate = 0.618033988749895

const (
	senderSaturation = 0.68
	senderLightness  = 0.66
	minHueGap        = 0.07 // ~25°: min distance from an avoided hue
)

// SpreadColors returns n sender colors stepped around the hue wheel by the golden
// ratio, skipping hues within minHueGap of any avoid color (e.g. pinned identities).
func (t Theme) SpreadColors(n int, avoid []color.Color) []color.Color {
	avoidHues := make([]float64, len(avoid))
	for i, c := range avoid {
		avoidHues[i] = hueOf(c)
	}
	out := make([]color.Color, 0, n)
	for k := 0; len(out) < n && k < 4096; k++ {
		hue := math.Mod(0.1+float64(k)*goldenRatioConjugate, 1)
		if !nearAnyHue(hue, avoidHues) {
			out = append(out, hslColor(hue*360, senderSaturation, senderLightness))
		}
	}
	return out
}

// nearAnyHue reports whether hue is within minHueGap of any avoided hue (wrapping at 1).
func nearAnyHue(hue float64, avoid []float64) bool {
	for _, a := range avoid {
		d := math.Abs(hue - a)
		if d > 0.5 {
			d = 1 - d
		}
		if d < minHueGap {
			return true
		}
	}
	return false
}

// hueOf returns the hue of c in [0,1).
func hueOf(c color.Color) float64 {
	r16, g16, b16, _ := c.RGBA()
	r, g, b := float64(r16)/0xffff, float64(g16)/0xffff, float64(b16)/0xffff
	maxc := math.Max(r, math.Max(g, b))
	minc := math.Min(r, math.Min(g, b))
	d := maxc - minc
	if d == 0 {
		return 0
	}
	var h float64
	switch maxc {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h /= 6
	if h < 0 {
		h++
	}
	return h
}

// ParseColor reads a color from a config string: a "#rrggbb" hex value or one of
// a few common names. ok is false for an empty or unrecognized value.
func ParseColor(s string) (color.Color, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if hex, ok := namedColors[strings.ToLower(s)]; ok {
		s = hex
	}
	if len(s) == 7 && s[0] == '#' && isHex(s[1:]) {
		return lipgloss.Color(s), true
	}
	return nil, false
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

// namedColors maps friendly config names to hex.
var namedColors = map[string]string{
	"red":     "#ff628c", // dark_pink: cobalt2's readable red, not its error #FF0000
	"green":   "#3ad900",
	"yellow":  "#ffc600",
	"blue":    "#00aaff",
	"magenta": "#967efb", // purple
	"cyan":    "#80fcff", // light_blue
	"orange":  "#ff9d00", // light_orange
	"white":   "#deebfe", // light_purple: cobalt2's near-white
}

// hslColor converts HSL (h in degrees, s/l in [0,1]) to a hex color.
func hslColor(h, s, l float64) color.Color {
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(h, 360) / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g, b = c, x, 0
	case hp < 2:
		r, g, b = x, c, 0
	case hp < 3:
		r, g, b = 0, c, x
	case hp < 4:
		r, g, b = 0, x, c
	case hp < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := l - c/2
	to8 := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", to8(r), to8(g), to8(b)))
}

// Swatch renders in color c, for a color picker.
func (t Theme) Swatch(c color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c)
}

// SpellMark underlines a flagged word without changing its colors: BadgeAlert for a
// misspelling, Badge for a rare-word hint. It is an ansi.Style, not a lipgloss.Style,
// because lipgloss renders underlines rune by rune, which splits grapheme clusters.
func (t Theme) SpellMark(underline ansi.Underline, hint bool) ansi.Style {
	color := t.Palette.BadgeAlert
	if hint {
		color = t.Palette.Badge
	}
	style := ansi.NewStyle().UnderlineColor(color)
	if underline == ansi.UnderlineSingle {
		// Plain SGR 4, which every terminal reads, rather than 4:1.
		return style.Underline(true)
	}
	return style.UnderlineStyle(underline)
}
