// Package theme is kith's palette and the lipgloss styles built from it. Its only
// internal import is themespec.
package theme

import (
	"fmt"
	"image/color"
	"math"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/themespec"
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

// Theme bundles the styles the views apply. Border styles carry no size; callers set
// Width/Height per frame.
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
	// minColorGap is how far, in OKLab, a sender color keeps from an avoided one. Hue
	// alone is not enough: at this lightness a yellow 47° from a green still reads as it.
	minColorGap = 0.12
)

// SpreadColors returns n sender colors stepped around the hue wheel by the golden
// ratio, skipping those that look close to any avoid color (e.g. pinned identities).
func (t Theme) SpreadColors(n int, avoid []color.Color) []color.Color {
	avoidLab := make([][3]float64, len(avoid))
	for i, c := range avoid {
		avoidLab[i] = oklab(c)
	}
	out := make([]color.Color, 0, n)
	for k := 0; len(out) < n && k < 4096; k++ {
		hue := math.Mod(0.1+float64(k)*goldenRatioConjugate, 1)
		c := hslColor(hue*360, senderSaturation, senderLightness)
		if !nearAny(oklab(c), avoidLab) {
			out = append(out, c)
		}
	}
	return out
}

// nearAny reports whether an OKLab color is within minColorGap of any avoided one.
func nearAny(lab [3]float64, avoid [][3]float64) bool {
	for _, a := range avoid {
		if colorDistance(lab, a) < minColorGap {
			return true
		}
	}
	return false
}

// colorDistance is the distance between two OKLab colors, which tracks how different
// they look.
func colorDistance(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

// oklab is c in OKLab (L, a, b), from sRGB.
func oklab(c color.Color) [3]float64 {
	r16, g16, b16, _ := c.RGBA()
	linear := func(v uint32) float64 {
		x := float64(v) / 0xffff
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	r, g, b := linear(r16), linear(g16), linear(b16)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// ParseColor reads a color from a config string: a "#rrggbb" hex value or one of
// a few common names. ok is false for an empty or unrecognized value.
func ParseColor(s string) (color.Color, bool) {
	hex, ok := themespec.Color(s)
	if !ok {
		return nil, false
	}
	return lipgloss.Color(hex), true
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
