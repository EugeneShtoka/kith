package qrtext_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rsc.io/qr"

	"github.com/EugeneShtoka/kith/internal/qrtext"
)

// Each character is two modules, the top one its upper half: the lines read back as
// the code itself, inside a light margin.
func TestLinesAreTheCode(t *testing.T) {
	t.Parallel()

	const data = "https://support.google.com/messages/?p=web_computer#?c=CiAKHgoQZXhhbXBsZS1wYWlyaW5nEgpleGFtcGxlLWtleQ=="
	lines, err := qrtext.Lines(data)
	if err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(data, qr.L)
	if err != nil {
		t.Fatal(err)
	}
	side := code.Size + 4
	if len(lines) != (side+1)/2 {
		t.Fatalf("%d lines for a %d-module side", len(lines), side)
	}
	for row, line := range lines {
		if n := utf8.RuneCountInString(line); n != side {
			t.Fatalf("line %d is %d wide, want %d", row, n, side)
		}
		for x, r := range []rune(line) {
			top, bottom := !code.Black(x-2, 2*row-2), !code.Black(x-2, 2*row-1)
			if 2*row+1 >= side {
				bottom = false // past the last row: drawn dark, as the background
			}
			want := map[[2]bool]rune{{true, true}: '█', {true, false}: '▀', {false, true}: '▄', {false, false}: ' '}[[2]bool{top, bottom}]
			if r != want {
				t.Fatalf("line %d, column %d is %q, want %q", row, x, r, want)
			}
		}
	}
	if !strings.HasPrefix(lines[0], "██") || strings.Trim(lines[0], "█") != "" {
		t.Errorf("the top margin is not light: %q", lines[0])
	}
}

// Too much data for any code is an error, not a panic.
func TestTooMuchData(t *testing.T) {
	t.Parallel()

	if _, err := qrtext.Lines(strings.Repeat("x", 5000)); err == nil {
		t.Error("5000 bytes encoded")
	}
}
