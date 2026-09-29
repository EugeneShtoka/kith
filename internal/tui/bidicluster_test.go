package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/bidi"
)

// Laying an RTL line out right-to-left must reverse grapheme *clusters*, never the
// runes inside one.
func TestReorderKeepsEmojiSequencesWhole(t *testing.T) {
	t.Parallel()

	sequences := []struct {
		name  string
		emoji string
	}{
		{"zwj sequence", "🙋‍♀️"},
		{"variation selector", "☝️"},
		{"skin tone", "👍🏽"},
		{"flag", "🇧🇬"},
		{"single rune", "🤣"},
		{"family", "👨‍👩‍👧"},
	}
	for _, seq := range sequences {
		t.Run(seq.name, func(t *testing.T) {
			t.Parallel()
			line := "שלום " + seq.emoji + " מה קורה"
			vis := reorder(line, bidi.RightToLeft)

			// The sequence survives as one piece, in one grapheme cluster.
			if !strings.Contains(vis, seq.emoji) {
				t.Errorf("the emoji was taken apart:\n in  %q\n out %q", line, vis)
			}
			if before, after := clusters(line), clusters(vis); before != after {
				t.Errorf("cluster count %d -> %d: a grapheme was split", before, after)
			}
			// And the line is still as wide as it was, which is what the pane was
			// measured against.
			if w1, w2 := ansi.StringWidth(line), ansi.StringWidth(vis); w1 != w2 {
				t.Errorf("width %d -> %d", w1, w2)
			}
		})
	}
}

// Bracket mirroring (rule L4) still applies: it lives on single runes, which is exactly
// what still goes through AppendReverse.
func TestReorderStillMirrorsBrackets(t *testing.T) {
	t.Parallel()

	vis := reorder("שלום (עולם) שלום", bidi.RightToLeft)
	if strings.Count(vis, "(") != 1 || strings.Count(vis, ")") != 1 {
		t.Errorf("brackets lost: %q", vis)
	}
}

func clusters(s string) int {
	n := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		n++
	}
	return n
}
