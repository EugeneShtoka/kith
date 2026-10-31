// Package theme is kith's palette and the lipgloss styles built from it. Its only
// internal import is themespec.
package theme

import (
	"fmt"
	"image/color"

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

// SpreadColors returns n sender colors stepped around the hue wheel by the golden
// ratio, skipping those that look close to any avoid color (e.g. pinned identities):
// themespec.SenderColors, which the desktop's relay hands its window too.
func (t Theme) SpreadColors(n int, avoid []color.Color) []color.Color {
	hexes := make([]string, len(avoid))
	for i, c := range avoid {
		r, g, b, _ := c.RGBA()
		hexes[i] = fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
	}
	spread := themespec.SenderColors(n, hexes)
	out := make([]color.Color, len(spread))
	for i, hex := range spread {
		out[i] = lipgloss.Color(hex)
	}
	return out
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
