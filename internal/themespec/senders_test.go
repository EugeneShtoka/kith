package themespec

import (
	"math"
	"testing"
)

// labOf is a "#rrggbb" color in OKLab, for tests that hand it one.
func labOf(t *testing.T, hex string) [3]float64 {
	t.Helper()
	lab, ok := oklab(hex)
	if !ok {
		t.Fatalf("%q is no #rrggbb color", hex)
	}
	return lab
}

// Generated colors keep clear of colors already pinned to identities, by how they
// look rather than by hue alone.
func TestSenderColorsAvoidPinnedColors(t *testing.T) {
	t.Parallel()

	pinned := []string{"#3ad900", "#00aaff"}
	got := SenderColors(40, pinned)

	if len(got) != 40 {
		t.Fatalf("got %d colors, want 40", len(got))
	}
	for i, c := range got {
		for j, p := range pinned {
			if d := colorDistance(labOf(t, c), labOf(t, p)); d < minColorGap {
				t.Errorf("color %d is %.3f from pinned color %d, want at least %.3f",
					i, d, j, minColorGap)
			}
		}
	}
}

// A room of three with "me" pinned light green: the slots numbered past it never land
// on a yellow or teal that reads as the same green, as slot 4's yellow did when only
// the hue was kept 25° away.
func TestSenderColorsKeepYellowsAwayFromALightGreen(t *testing.T) {
	t.Parallel()

	me := "#8ff586"
	for i, c := range SenderColors(12, []string{me}) {
		if d := colorDistance(labOf(t, c), labOf(t, me)); d < 0.1 {
			t.Errorf("slot %d (%v) is %.3f from %v: it reads as the same color", i, c, d, me)
		}
	}
}

// Consecutive senders get colors that look different.
func TestSenderColorsSeparateNeighbours(t *testing.T) {
	t.Parallel()

	got := SenderColors(12, nil)
	for i := 1; i < len(got); i++ {
		if d := colorDistance(labOf(t, got[i]), labOf(t, got[i-1])); d < 0.2 {
			t.Errorf("colors %d and %d are %.3f apart; they will read as the same", i-1, i, d)
		}
	}
}

// All generated colors are distinct, not just neighbors.
func TestSenderColorsAreAllDifferent(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, c := range SenderColors(64, nil) {
		if seen[c] {
			t.Fatalf("%v was handed out twice", c)
		}
		seen[c] = true
	}
}

// n == 0 and an empty avoid list are handled.
func TestSenderColorsDegenerateInputs(t *testing.T) {
	t.Parallel()

	if got := SenderColors(0, nil); len(got) != 0 {
		t.Errorf("SenderColors(0) = %v, want none", got)
	}
	if got := SenderColors(1, nil); len(got) != 1 {
		t.Errorf("SenderColors(1) returned %d colors", len(got))
	}
}

// An avoid list covering the wheel returns fewer colors instead of looping.
func TestSenderColorsGiveUpRatherThanSpinning(t *testing.T) {
	t.Parallel()

	var everywhere []string
	for h := 0.0; h < 1; h += 0.01 {
		everywhere = append(everywhere, hslColor(h*360, senderSaturation, senderLightness))
	}
	got := SenderColors(8, everywhere)
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
		if got := hslColor(tc.h, tc.s, tc.l); got != tc.want {
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
		lab := labOf(t, tc.hex)
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
	if !nearAny(labOf(t, "#8ff586"), [][3]float64{labOf(t, "#88f080")}) {
		t.Error("two near-identical greens are not near")
	}
	if nearAny(labOf(t, "#8ff586"), [][3]float64{labOf(t, "#a07ff0")}) {
		t.Error("a green and a violet are near")
	}
	if nearAny(labOf(t, "#8ff586"), nil) {
		t.Error("nothing to avoid, yet near")
	}
	for _, bad := range []string{"", "#fff", "8ff586", "#8ff58g"} {
		if _, ok := oklab(bad); ok {
			t.Errorf("oklab(%q) read a color", bad)
		}
	}
}
