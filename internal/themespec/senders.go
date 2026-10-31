package themespec

import (
	"fmt"
	"math"
	"strconv"
)

// Golden-ratio hue steps keep consecutive sender colors maximally distinct.
const goldenRatioConjugate = 0.618033988749895

const (
	senderSaturation = 0.68
	senderLightness  = 0.66
	// minColorGap is how far, in OKLab, a sender color keeps from an avoided one. Hue
	// alone is not enough: at this lightness a yellow 47° from a green still reads as it.
	minColorGap = 0.12
)

// SenderColors returns n sender colors as "#rrggbb", stepped around the hue wheel by the
// golden ratio, skipping those that look close to any avoid color ("#rrggbb", e.g.
// pinned identities'). A room's sender in slot i is in its i-th color: the TUI's and
// the desktop's alike.
func SenderColors(n int, avoid []string) []string {
	avoidLab := make([][3]float64, 0, len(avoid))
	for _, c := range avoid {
		if lab, ok := oklab(c); ok {
			avoidLab = append(avoidLab, lab)
		}
	}
	out := make([]string, 0, n)
	for k := 0; len(out) < n && k < 4096; k++ {
		hue := math.Mod(0.1+float64(k)*goldenRatioConjugate, 1)
		c := hslColor(hue*360, senderSaturation, senderLightness)
		if lab, _ := oklab(c); !nearAny(lab, avoidLab) {
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

// oklab is a "#rrggbb" color in OKLab (L, a, b), from sRGB; false for anything else.
func oklab(hex string) ([3]float64, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return [3]float64{}, false
	}
	rgb, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return [3]float64{}, false
	}
	linear := func(v uint64) float64 {
		x := float64(v) / 0xff
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	r, g, b := linear(rgb>>16&0xff), linear(rgb>>8&0xff), linear(rgb&0xff)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}, true
}

// hslColor converts HSL (h in degrees, s/l in [0,1]) to "#rrggbb".
func hslColor(h, s, l float64) string {
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
	return fmt.Sprintf("#%02x%02x%02x", to8(r), to8(g), to8(b))
}
