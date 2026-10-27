package main

import (
	"strings"
	"testing"
)

// A QR code is drawn light on dark, each of its lines colored on its own, between blank
// lines; data no code can hold says why instead.
func TestAQRCodeIsDrawnLightOnDark(t *testing.T) {
	t.Parallel()
	block := qrBlock("https://example.org/pair#abc")
	if len(block) < 12 || block[0].text != "" || block[len(block)-1].text != "" {
		t.Fatalf("drew %d lines, want the code between blank lines", len(block))
	}
	for _, line := range block[1 : len(block)-1] {
		if !strings.Contains(line.text, "█") && !strings.Contains(line.text, "▀") && !strings.Contains(line.text, "▄") {
			continue
		}
		if !strings.Contains(line.text, "\x1b[97;40m") || !strings.HasSuffix(line.text, "\x1b[0m") {
			t.Fatalf("a line not drawn light on dark: %q", line.text)
		}
		if want := len([]rune(strings.TrimSuffix(strings.TrimPrefix(line.text, "    \x1b[97;40m"), "\x1b[0m"))) + 4; line.shown != want {
			t.Errorf("a line shows %d columns, counted %d", want, line.shown)
		}
	}
	if block := qrBlock(strings.Repeat("x", 5000)); len(block) != 3 || !strings.Contains(block[1].text, "could not be drawn") {
		t.Errorf("too much data drew %+v", block)
	}
}
