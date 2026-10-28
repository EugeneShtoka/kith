package theme

import (
	"image/color"
	"math"
	"testing"

	"charm.land/lipgloss/v2"
)

// Generated colors keep clear of colors already pinned to identities, by how they
// look rather than by hue alone.
func TestSpreadColorsAvoidsPinnedColors(t *testing.T) {
	t.Parallel()

	pinned := []color.Color{lipgloss.Color("#3ad900"), lipgloss.Color("#00aaff")}
	got := New(Default()).SpreadColors(40, pinned)

	if len(got) != 40 {
		t.Fatalf("got %d colors, want 40", len(got))
	}
	for i, c := range got {
		for j, p := range pinned {
			if d := colorDistance(oklab(c), oklab(p)); d < minColorGap {
				t.Errorf("color %d is %.3f from pinned color %d, want at least %.3f",
					i, d, j, minColorGap)
			}
		}
	}
}

// A room of three with "me" pinned light green: the slots numbered past it never land
// on a yellow or teal that reads as the same green, as slot 4's yellow did when only
// the hue was kept 25° away.
func TestSpreadColorsKeepYellowsAwayFromALightGreen(t *testing.T) {
	t.Parallel()

	me := lipgloss.Color("#8ff586")
	for i, c := range New(Default()).SpreadColors(12, []color.Color{me}) {
		if d := colorDistance(oklab(c), oklab(me)); d < 0.1 {
			t.Errorf("slot %d (%v) is %.3f from %v: it reads as the same color", i, c, d, me)
		}
	}
}

// Consecutive senders get colors that look different.
func TestSpreadColorsSeparatesNeighbours(t *testing.T) {
	t.Parallel()

	got := New(Default()).SpreadColors(12, nil)
	for i := 1; i < len(got); i++ {
		if d := colorDistance(oklab(got[i]), oklab(got[i-1])); d < 0.2 {
			t.Errorf("colors %d and %d are %.3f apart; they will read as the same", i-1, i, d)
		}
	}
}

// All generated colors are distinct, not just neighbors.
func TestSpreadColorsAreAllDifferent(t *testing.T) {
	t.Parallel()

	seen := map[color.Color]bool{}
	for _, c := range New(Default()).SpreadColors(64, nil) {
		if seen[c] {
			t.Fatalf("%v was handed out twice", c)
		}
		seen[c] = true
	}
}

// n == 0 and an empty avoid list are handled.
func TestSpreadColorsDegenerateInputs(t *testing.T) {
	t.Parallel()

	if got := New(Default()).SpreadColors(0, nil); len(got) != 0 {
		t.Errorf("SpreadColors(0) = %v, want none", got)
	}
	if got := New(Default()).SpreadColors(1, nil); len(got) != 1 {
		t.Errorf("SpreadColors(1) returned %d colors", len(got))
	}
}

// An avoid list covering the wheel returns fewer colors instead of looping.
func TestSpreadColorsGivesUpRatherThanSpinning(t *testing.T) {
	t.Parallel()

	var everywhere []color.Color
	for h := 0.0; h < 1; h += 0.01 {
		everywhere = append(everywhere, hslColor(h*360, senderSaturation, senderLightness))
	}
	got := New(Default()).SpreadColors(8, everywhere)
	if len(got) != 0 {
		t.Errorf("got %d colors despite every hue being taken", len(got))
	}
}

// hslColor on the primaries, a mid-hue and wraparound.
func TestHSLConversion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		h, s, l float64
		want    string
	}{
		{"red", 0, 1, 0.5, "#ff0000"},
		{"green", 120, 1, 0.5, "#00ff00"},
		{"blue", 240, 1, 0.5, "#0000ff"},
		{"cyan", 180, 1, 0.5, "#00ffff"},
		{"yellow", 60, 1, 0.5, "#ffff00"},
		{"magenta", 300, 1, 0.5, "#ff00ff"},
		{"black", 0, 0, 0, "#000000"},
		{"white", 0, 0, 1, "#ffffff"},
		{"mid grey", 210, 0, 0.5, "#808080"},
		// 480° wraps to 120°.
		{"wraps past 360", 480, 1, 0.5, "#00ff00"},
	} {
		if got := hslColor(tc.h, tc.s, tc.l); got != lipgloss.Color(tc.want) {
			t.Errorf("%s: hslColor(%g, %g, %g) = %v, want %s", tc.name, tc.h, tc.s, tc.l, got, tc.want)
		}
	}
}

// OKLab puts black, white and grey on the lightness axis, and measures a color as
// nothing from itself.
func TestOKLab(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		hex  string
		l    float64
		grey bool
	}{
		{"#000000", 0, true}, {"#ffffff", 1, true}, {"#808080", 0.6, true}, {"#ff0000", 0.628, false},
	} {
		lab := oklab(lipgloss.Color(tc.hex))
		if math.Abs(lab[0]-tc.l) > 0.01 {
			t.Errorf("%s: L = %.3f, want %.3f", tc.hex, lab[0], tc.l)
		}
		if chroma := math.Hypot(lab[1], lab[2]); tc.grey && chroma > 0.001 {
			t.Errorf("%s: a grey has chroma %.4f", tc.hex, chroma)
		}
		if d := colorDistance(lab, lab); d != 0 {
			t.Errorf("%s: %.4f from itself", tc.hex, d)
		}
	}
	if !nearAny(oklab(lipgloss.Color("#8ff586")), [][3]float64{oklab(lipgloss.Color("#88f080"))}) {
		t.Error("two near-identical greens are not near")
	}
	if nearAny(oklab(lipgloss.Color("#8ff586")), [][3]float64{oklab(lipgloss.Color("#a07ff0"))}) {
		t.Error("a green and a violet are near")
	}
	if nearAny(oklab(lipgloss.Color("#8ff586")), nil) {
		t.Error("nothing to avoid, yet near")
	}
}

// ParseColor accepts hex and the named colors, nothing that merely looks like one.
func TestParseColor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want string // "" means it must be refused
	}{
		{"#00aaff", "#00aaff"},
		{"  #00aaff  ", "#00aaff"},
		{"#ABCDEF", "#ABCDEF"},
		{"green", "#3ad900"},
		{"GREEN", "#3ad900"},
		{"teal", "#1de9b6"},
		{" Orange ", "#ff9d00"},
		{"", ""},
		{"   ", ""},
		{"#00aaf", ""},   // one short
		{"#00aafff", ""}, // one long
		{"00aaff", ""},   // no hash
		{"#00aagg", ""},  // not hex
		{"chartreuse", ""},
		{"0x00aaff", ""},
	} {
		got, ok := ParseColor(tc.in)
		if tc.want == "" {
			if ok {
				t.Errorf("ParseColor(%q) = %v, want refused", tc.in, got)
			}
			continue
		}
		if !ok {
			t.Errorf("ParseColor(%q) was refused, want %s", tc.in, tc.want)
		} else if got != lipgloss.Color(tc.want) {
			t.Errorf("ParseColor(%q) = %v, want %s", tc.in, got, tc.want)
		}
	}
}

// A PaneRow selection keeps its (dimmer) fill when the pane loses focus.
func TestPaneRowKeepsItsFillWhenUnfocused(t *testing.T) {
	t.Parallel()

	th := New(Default())
	focused := th.PaneRow(true, true)
	unfocused := th.PaneRow(true, false)
	plain := th.PaneRow(false, false)

	if focused.GetBackground() != th.Palette.SelectedBG {
		t.Error("a focused selected row is not filled with SelectedBG")
	}
	if unfocused.GetBackground() != th.Palette.SelectedDimBG {
		t.Error("an unfocused selected row lost its fill; the cursor becomes unfindable")
	}
	if focused.GetBackground() == unfocused.GetBackground() {
		t.Error("both fills are the same, so two selections on screen compete")
	}
	if plain.GetBackground() != unset {
		t.Errorf("an unselected row is filled with %v", plain.GetBackground())
	}
}

// Overlay rows show selection in the foreground, never a fill.
func TestRowNeverFills(t *testing.T) {
	t.Parallel()

	th := New(Default())
	for _, tc := range []struct{ selected, focused bool }{
		{true, true}, {true, false}, {false, true}, {false, false},
	} {
		if home := th.Row(tc.selected, tc.focused).GetBackground(); home != unset {
			t.Errorf("Row(%v, %v) has background %v; overlay rows are foreground-only",
				tc.selected, tc.focused, home)
		}
	}
	if th.Row(true, true).GetForeground() == th.Row(false, false).GetForeground() {
		t.Error("a selected overlay row looks exactly like an unselected one")
	}
}

// The marker carries the row's fill and the cursor color, focused or not.
func TestMarkerSitsOnTheRowsFill(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if got := th.Marker(true, true); got.GetBackground() != th.Palette.SelectedBG {
		t.Error("the marker punches a hole in the focused row's fill")
	}
	if got := th.Marker(true, true); got.GetForeground() != th.Palette.Cursor {
		t.Error("the focused marker is not the cursor color")
	}
	if got := th.Marker(true, false); got.GetForeground() != th.Palette.Muted {
		t.Error("the unfocused marker should recede, not disappear")
	}
	// Unselected, the marker is just the row's style.
	marker, row := th.Marker(false, true), th.PaneRow(false, true)
	if marker.GetForeground() != row.GetForeground() ||
		marker.GetBackground() != row.GetBackground() {
		t.Error("an unselected marker differs from the row it sits in")
	}
}

// A badge is repainted onto the row's fill so it doesn't punch a hole in it.
func TestOnPaneRowRepaintsOntoTheFill(t *testing.T) {
	t.Parallel()

	th := New(Default())
	badge := th.Badge(false)
	if got := th.OnPaneRow(badge, true, true); got.GetBackground() != th.Palette.SelectedBG {
		t.Error("a badge on a focused selected row did not take its fill")
	}
	if got := th.OnPaneRow(badge, true, false); got.GetBackground() != th.Palette.SelectedDimBG {
		t.Error("a badge on an unfocused selected row did not take its fill")
	}
	if got := th.OnPaneRow(badge, false, false); got.GetBackground() != unset {
		t.Error("a badge on an unselected row was given a background")
	}
	if th.OnPaneRow(badge, true, true).GetForeground() != badge.GetForeground() {
		t.Error("repainting the background changed the badge's own color")
	}
}

// A highlight (mention) badge differs from an ordinary unread count.
func TestHighlightBadgeIsItsOwnColor(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if th.Badge(true).GetForeground() == th.Badge(false).GetForeground() {
		t.Error("a highlight badge is the same color as a plain one")
	}
	if th.Badge(true).GetForeground() != th.Palette.BadgeAlert {
		t.Error("the highlight badge is not BadgeAlert")
	}
}

// Focused and unfocused frames differ; it is the only focus cue.
func TestFocusIsVisible(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if th.Pane.GetBorderTopForeground() == th.PaneActive.GetBorderTopForeground() {
		t.Error("the focused and unfocused pane frames are the same color")
	}
	if th.Title.GetForeground() == th.TitleActive.GetForeground() {
		t.Error("the focused and unfocused pane titles are the same color")
	}
	if th.PaneActive.GetBorderTopForeground() != th.Palette.Accent {
		t.Error("the focused frame is not the accent color")
	}
	if th.Palette != Default() {
		t.Error("New did not carry the palette through")
	}
}

// unset is what lipgloss reports for a style with no background.
var unset = lipgloss.NewStyle().GetBackground()
