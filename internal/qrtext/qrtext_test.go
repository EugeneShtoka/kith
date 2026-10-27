package qrtext_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rsc.io/qr"

	"github.com/EugeneShtoka/kith/internal/qrtext"
)

// A Telegram login's code (a tg://login URL, its token generously long) fits an 80×24
// terminal; a WhatsApp pairing's (four base64 parts, ~225 characters) fits 80 columns
// and needs 31 rows with its blank lines, which docs/whatsapp.md says.
func TestPhoneLoginCodesFitATerminal(t *testing.T) {
	t.Parallel()
	telegram := "tg://login?token=" + strings.Repeat("A", 88) // 64 bytes, base64url
	whatsapp := "2@" + strings.Repeat("A", 88) + "," + strings.Repeat("A", 44) + "," +
		strings.Repeat("A", 44) + "," + strings.Repeat("A", 44)
	for _, tc := range []struct {
		name       string
		data       string
		cols, rows int
	}{
		{"telegram", telegram, 80, 24 - 2},
		{"whatsapp", whatsapp, 80, 31 - 2},
	} {
		lines, err := qrtext.Lines(tc.data)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if w := utf8.RuneCountInString(lines[0]); w > tc.cols || len(lines) > tc.rows {
			t.Errorf("%s: %d columns × %d lines, want within %d × %d", tc.name, w, len(lines), tc.cols, tc.rows)
		}
	}
}

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
