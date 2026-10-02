package whatsapp

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// WhatsApp's markers become formatting, as WhatsApp itself reads them; the words are
// kept without them.
func TestWhatsAppMarkersBecomeFormatting(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in, words string
		spans     []richtext.Span // only the marks that matter; nil for none
	}{
		{"plain words", "plain words", nil},
		{"*bold* and _it_", "bold and it", []richtext.Span{{Start: 0, End: 4, Bold: true}, {Start: 9, End: 11, Italic: true}}},
		{"~gone~", "gone", []richtext.Span{{Start: 0, End: 4, Strike: true}}},
		{"*_both_*", "both", []richtext.Span{{Start: 0, End: 4, Bold: true}, {Start: 0, End: 4, Italic: true}}},
		{"run `ls -l` now", "run ls -l now", []richtext.Span{{Start: 4, End: 9, Code: true}}},
		{"```\n*not bold*\n```", "*not bold*", []richtext.Span{{Start: 0, End: 10, Code: true}}},
		// Not markers: inside a word, around spaces, alone, across lines.
		{"2*3*4", "2*3*4", nil},
		{"a * b * c", "a * b * c", nil},
		{"snake_case_name", "snake_case_name", nil},
		{"*", "*", nil},
		{"*one\ntwo*", "*one\ntwo*", nil},
		// Markup in the text is words, not markup.
		{"<b>hi</b> *x*", "<b>hi</b> x", []richtext.Span{{Start: 10, End: 11, Bold: true}}},
	} {
		words, f := formatted(c.in)
		if words != c.words {
			t.Errorf("%q: words = %q, want %q", c.in, words, c.words)
		}
		if c.spans == nil {
			if !f.IsZero() {
				t.Errorf("%q: formatted as %q, want plain", c.in, f.Markup())
			}
			continue
		}
		if f.Text() != c.words {
			t.Errorf("%q: formatted text = %q", c.in, f.Text())
		}
		got := f.Spans()
		for _, want := range c.spans {
			if !slices.ContainsFunc(got, func(s richtext.Span) bool {
				return s.Start == want.Start && s.End == want.End && s.Bold == want.Bold &&
					s.Italic == want.Italic && s.Strike == want.Strike && s.Code == want.Code
			}) {
				t.Errorf("%q: spans %+v lack %+v", c.in, got, want)
			}
		}
	}
}

// What kith's composer writes in Markdown goes out in WhatsApp's markers.
func TestMarkdownGoesOutAsWhatsAppWritesIt(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"**bold** and *it*":          "*bold* and _it_",
		"__bold__ _it_":              "*bold* _it_",
		"~~gone~~":                   "~gone~",
		"see [the doc](https://x.y)": "see the doc (https://x.y)",
		"[https://x.y](https://x.y)": "https://x.y",
		"`**not bold**` **bold**":    "`**not bold**` *bold*",
		"```\n**as is**\n```":        "```\n**as is**\n```",
		"2*3*4 and a * b":            "2*3*4 and a * b",
		"plain":                      "plain",
	} {
		if got := fromMarkdown(in); got != want {
			t.Errorf("fromMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

// Whatever is typed, the formatting draws over the words exactly: the same text, and
// every span inside it.
func TestFormattingAlwaysFitsItsWords(t *testing.T) {
	t.Parallel()
	alphabet := []string{"*", "_", "~", "`", "```", " ", "\n", "a", "b", "é", "<", "&"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		var b strings.Builder
		for range rng.IntN(24) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		in := b.String()
		words, f := formatted(in)
		if f.IsZero() {
			if words != in {
				t.Fatalf("%q: plain, but the words changed to %q", in, words)
			}
			continue
		}
		if f.Text() != words {
			t.Fatalf("%q: words %q, formatted text %q", in, words, f.Text())
		}
		for _, s := range f.Spans() {
			if s.Start < 0 || s.End > len(words) || s.Start > s.End {
				t.Fatalf("%q: span %+v outside %q", in, s, words)
			}
		}
	}
}
