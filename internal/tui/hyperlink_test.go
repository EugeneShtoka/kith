package tui

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// A link's run is wrapped in OSC 8 so the terminal can open it, and the wrapper is
// zero-width — the row still measures as its text.
func TestHyperlinkWrapsTheRunWithoutWidth(t *testing.T) {
	t.Parallel()

	const text = "see https://example.org/docs now"
	spans := bareLinkSpans(text, nil)
	if len(spans) != 1 || spans[0].Href != "https://example.org/docs" {
		t.Fatalf("spans = %+v, want one link span", spans)
	}
	r := richStyle{spans: spans, hyperlinks: true}
	row := styledRow(text, bidi.LeftToRight, r.at)
	if !strings.Contains(row, ansi.SetHyperlink("https://example.org/docs")) {
		t.Error("the row carries no OSC 8 opener")
	}
	if !strings.Contains(row, ansi.ResetHyperlink()) {
		t.Error("the row never closes the hyperlink")
	}
	if got, want := ansi.StringWidth(row), ansi.StringWidth(text); got != want {
		t.Errorf("width = %d, want %d — the escapes are being measured", got, want)
	}
}

// Off means the row is exactly what it was.
func TestHyperlinkCanBeTurnedOff(t *testing.T) {
	t.Parallel()

	const text = "see https://example.org/docs now"
	r := richStyle{spans: bareLinkSpans(text, nil)}
	if row := styledRow(text, bidi.LeftToRight, r.at); strings.Contains(row, "\x1b]8;") {
		t.Errorf("row = %q, want no hyperlink escapes with the setting off", row)
	}
}

// Two links in one message point at two different places, which they cannot do if their
// runs coalesce on having the same marks.
func TestHyperlinkKeepsTwoLinksApart(t *testing.T) {
	t.Parallel()

	const text = "https://a.example https://b.example"
	spans := bareLinkSpans(text, nil)
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want two", spans)
	}
	r := richStyle{spans: spans, hyperlinks: true}
	row := styledRow(text, bidi.LeftToRight, r.at)
	for _, want := range []string{"https://a.example", "https://b.example"} {
		if !strings.Contains(row, ansi.SetHyperlink(want)) {
			t.Errorf("row does not point at %s", want)
		}
	}
}

// The sender's own markup wins where both would claim the same text: its href may
// differ from the words, and two hyperlink runs on one cell has no good answer.
func TestHyperlinkDoesNotDoubleUpOnAMarkedLink(t *testing.T) {
	t.Parallel()

	const text = "https://example.org"
	have := []richtext.Span{{Start: 0, End: len(text), Link: true, Href: "https://elsewhere.example"}}
	if got := bareLinkSpans(text, have); got != nil {
		t.Errorf("bare spans = %+v, want none where the sender already linked it", got)
	}
}

// A mention pill links to the place it names (this binary handles `matrix:` links).
func TestHyperlinkOnAMentionPill(t *testing.T) {
	t.Parallel()

	const text = "ask Dana about it"
	r := richStyle{
		mentions:   []mentionRange{{start: 4, end: 8, href: "matrix:u/dana:example.org"}},
		hyperlinks: true,
	}
	row := styledRow(text, bidi.LeftToRight, r.at)
	if !strings.Contains(row, ansi.SetHyperlink("matrix:u/dana:example.org")) {
		t.Errorf("row = %q, want the pill to link to the person", row)
	}
	if got, want := ansi.StringWidth(row), ansi.StringWidth(text); got != want {
		t.Errorf("width = %d, want %d", got, want)
	}
}

// The sender's own markup outranks our reading of the words: an <a href> covering a
// pill keeps its target, because they wrote that address and we inferred the other.
func TestHyperlinkSpanBeatsThePill(t *testing.T) {
	t.Parallel()

	const text = "ask Dana about it"
	r := richStyle{
		mentions:   []mentionRange{{start: 4, end: 8, href: "matrix:u/dana:example.org"}},
		spans:      []richtext.Span{{Start: 4, End: 8, Link: true, Href: "https://example.org/dana"}},
		hyperlinks: true,
	}
	row := styledRow(text, bidi.LeftToRight, r.at)
	if !strings.Contains(row, ansi.SetHyperlink("https://example.org/dana")) {
		t.Error("the sender's href lost to the inferred one")
	}
	if strings.Contains(row, ansi.SetHyperlink("matrix:u/dana:example.org")) {
		t.Error("both links were emitted for the same cells")
	}
}

// A message holding a link is drawn in its sender's color all through, not only
// where it holds none: the words after the link (on its next line, left-to-right or
// right-to-left) and before it on the same line.
func TestTheSendersColorOutlivesALink(t *testing.T) {
	t.Parallel()
	m := sized(t, newModel())
	m.prefs.display.ColorMessages = true
	yellow := color.RGBA{R: 255, G: 255, A: 255}
	tint := ansi.Style{}.ForegroundColor(yellow).String()
	for _, body := range []string{
		"https://x.com/a/status/1\nComplain that it rots",
		"https://x.com/a/status/1\nהחבר היה הראשון לדחות",
		"look at https://x.com/a/status/1",
	} {
		msg := domain.Message{ID: "$m", Sender: "@evyatar:x", Body: body}
		rows := m.messageRows(msg, 80, 12, map[string]color.Color{"@evyatar:x": yellow}, false, "")
		for _, row := range rows {
			// The link itself is in the link color; what is beside it is the sender's.
			words := strings.TrimSpace(strings.ReplaceAll(stripStyles(row), "https://x.com/a/status/1", ""))
			if words != "" && words != "evyatar" && !strings.Contains(row, tint) {
				t.Errorf("%q: the row %q is not in the sender's color:\n  %q", body, words, row)
			}
		}
	}
}
