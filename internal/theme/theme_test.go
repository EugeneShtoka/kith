package theme

import (
	"image/color"
	"math"
	"testing"

	"charm.land/lipgloss/v2"
)

// A generated color has to stay away from the hues an identity has already pinned, or
// the person you gave green to shares it with whoever the wheel lands on next.
func TestSpreadColorsAvoidsPinnedHues(t *testing.T) {
	t.Parallel()

	pinned := []color.Color{lipgloss.Color("#3ad900"), lipgloss.Color("#00aaff")}
	got := New(Default()).SpreadColors(40, pinned)

	if len(got) != 40 {
		t.Fatalf("got %d colors, want 40", len(got))
	}
	for i, c := range got {
		for j, p := range pinned {
			if d := hueDistance(hueOf(c), hueOf(p)); d < minHueGap {
				t.Errorf("color %d is %.3f from pinned color %d, want at least %.3f",
					i, d, j, minHueGap)
			}
		}
	}
}

// Consecutive senders must not get neighboring shades — that is the whole reason the
// step is the golden ratio rather than 1/n.
func TestSpreadColorsSeparatesNeighbours(t *testing.T) {
	t.Parallel()

	got := New(Default()).SpreadColors(12, nil)
	for i := 1; i < len(got); i++ {
		if d := hueDistance(hueOf(got[i]), hueOf(got[i-1])); d < 0.15 {
			t.Errorf("colors %d and %d are %.3f apart on the wheel; they will read as the same",
				i-1, i, d)
		}
	}
}

// Distinct, and distinct all the way down the list — not just between neighbors.
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

// Asking for none is not an error, and asking to avoid nothing is the ordinary case.
func TestSpreadColorsDegenerateInputs(t *testing.T) {
	t.Parallel()

	if got := New(Default()).SpreadColors(0, nil); len(got) != 0 {
		t.Errorf("SpreadColors(0) = %v, want none", got)
	}
	if got := New(Default()).SpreadColors(1, nil); len(got) != 1 {
		t.Errorf("SpreadColors(1) returned %d colors", len(got))
	}
}

// The search is bounded, so an avoid list covering the wheel returns fewer colors rather
// than looping forever.
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

// hslColor is the only place a color is computed rather than written down, so the three
// corners of the cube and one mid-hue are worth pinning.
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
		// Past the wheel: 480° is 120° again, which is what the Mod is for.
		{"wraps past 360", 480, 1, 0.5, "#00ff00"},
	} {
		if got := hslColor(tc.h, tc.s, tc.l); got != lipgloss.Color(tc.want) {
			t.Errorf("%s: hslColor(%g, %g, %g) = %v, want %s", tc.name, tc.h, tc.s, tc.l, got, tc.want)
		}
	}
}

// hueOf inverts hslColor's hue; SpreadColors depends on the two agreeing.
func TestHueOfInvertsHSL(t *testing.T) {
	t.Parallel()

	for deg := 0; deg < 360; deg += 15 {
		want := float64(deg) / 360
		got := hueOf(hslColor(float64(deg), senderSaturation, senderLightness))
		if hueDistance(got, want) > 0.01 {
			t.Errorf("%d°: read back as %.3f, want %.3f", deg, got, want)
		}
	}
}

// A grey has no hue, and asking for one must not divide by the zero chroma.
func TestHueOfGreyIsZero(t *testing.T) {
	t.Parallel()

	for _, c := range []color.Color{
		lipgloss.Color("#000000"), lipgloss.Color("#808080"), lipgloss.Color("#ffffff"),
	} {
		if got := hueOf(c); got != 0 {
			t.Errorf("hueOf(%v) = %v, want 0 for a grey", c, got)
		}
	}
}

// The wheel wraps, so 0.99 and 0.01 are close.
func TestNearAnyHueWrapsAroundTheWheel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		hue   float64
		avoid []float64
		want  bool
	}{
		{"nothing to avoid", 0.5, nil, false},
		{"same hue", 0.5, []float64{0.5}, true},
		{"just inside the gap", 0.5, []float64{0.5 + minHueGap/2}, true},
		{"just outside the gap", 0.5, []float64{0.5 + minHueGap*1.5}, false},
		{"across the seam", 0.99, []float64{0.01}, true},
		{"across the seam, far", 0.75, []float64{0.01}, false},
		{"second entry matches", 0.5, []float64{0.1, 0.5}, true},
	} {
		if got := nearAnyHue(tc.hue, tc.avoid); got != tc.want {
			t.Errorf("%s: nearAnyHue(%v, %v) = %v, want %v", tc.name, tc.hue, tc.avoid, got, tc.want)
		}
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

// Every friendly name has to survive ParseColor, or a config using one draws in the zero
// color.
func TestEveryNamedColorParses(t *testing.T) {
	t.Parallel()

	for name := range namedColors {
		if _, ok := ParseColor(name); !ok {
			t.Errorf("named color %q does not parse", name)
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

// The marker carries the row's fill so the selection runs unbroken beneath it, and its
// own color so it still says "here" in a pane that does not have the focus.
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
	// Unselected: the marker is not drawn, so it must be the row untouched.
	marker, row := th.Marker(false, true), th.PaneRow(false, true)
	if marker.GetForeground() != row.GetForeground() ||
		marker.GetBackground() != row.GetBackground() {
		t.Error("an unselected marker differs from the row it sits in")
	}
}

// A badge with a color of its own has to be repainted onto the row's fill, or a selected
// room's unread count shows as a hole in the highlight.
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

// A highlight badge — a mention — must not look like an ordinary unread count; that
// difference is the only thing distinguishing "someone said your name" at a glance.
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

// Focused and unfocused frames must differ: it is the only cue for which pane has keys.
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

func TestSwatch(t *testing.T) {
	t.Parallel()

	c := lipgloss.Color("#123456")
	if got := New(Default()).Swatch(c).GetForeground(); got != c {
		t.Errorf("Swatch = %v, want the color it was given", got)
	}
}

// unset is what lipgloss reports for a style with no background.
var unset = lipgloss.NewStyle().GetBackground()

// hueDistance is the shortest way round the wheel between two hues.
func hueDistance(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 0.5 {
		d = 1 - d
	}
	return d
}
