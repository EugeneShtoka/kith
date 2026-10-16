// Package qrtext draws a QR code as lines of text, two rows of modules to a line in
// half blocks, for a phone to scan off a terminal.
package qrtext

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// quiet is the margin of light modules around the code. The standard asks for four;
// two scan as well off a screen and keep the code inside a small terminal.
const quiet = 2

// Lines is data as a QR code: each line is two rows of modules. A character's
// foreground is a light module and its background a dark one, so the caller draws the
// lines light on dark (Light on Dark), whatever the terminal's own colors.
func Lines(data string) ([]string, error) {
	code, err := qr.Encode(data, qr.L)
	if err != nil {
		return nil, fmt.Errorf("qrtext: encode: %w", err)
	}
	light := func(x, y int) bool { return !code.Black(x-quiet, y-quiet) } // outside the code is light
	side := code.Size + 2*quiet
	lines := make([]string, 0, (side+1)/2)
	for y := 0; y < side; y += 2 {
		var b strings.Builder
		for x := range side {
			top, bottom := light(x, y), y+1 < side && light(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		lines = append(lines, b.String())
	}
	return lines, nil
}

// The colors to draw the lines in: light modules white, dark ones black.
const (
	Light = "#ffffff"
	Dark  = "#000000"
)
