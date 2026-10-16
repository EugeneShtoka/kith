package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// stdout is what fn prints.
func stdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = was
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A QR code prints light on dark, each line colored on its own; data no code can hold
// prints why instead.
func TestPrintQR(t *testing.T) { //nolint:paralleltest // swaps os.Stdout
	out := stdout(t, func() { printQR("https://example.org/pair#abc") })
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 10 || !strings.Contains(out, "█") {
		t.Fatalf("printed %q", out)
	}
	for _, line := range lines {
		if !strings.Contains(line, "\x1b[97;40m") || !strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("a line not drawn light on dark: %q", line)
		}
	}
	if out := stdout(t, func() { printQR(strings.Repeat("x", 5000)) }); !strings.Contains(out, "could not be drawn") {
		t.Errorf("too much data printed %q", out)
	}
}
