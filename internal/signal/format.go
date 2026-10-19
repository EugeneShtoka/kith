package signal

import (
	"slices"
	"strings"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/markdown"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Signal formats a body by styles over UTF-16 ranges of its text, U+FFFC and all: bold,
// italic, strikethrough, monospace and spoilers. It has no links, headings or quotes.

// tags is each style's markup.
var tags = map[signalv1.Style_Kind][2]string{
	signalv1.Style_KIND_BOLD:          {"<b>", "</b>"},
	signalv1.Style_KIND_ITALIC:        {"<i>", "</i>"},
	signalv1.Style_KIND_STRIKETHROUGH: {"<del>", "</del>"},
	signalv1.Style_KIND_MONOSPACE:     {"<code>", "</code>"},
	signalv1.Style_KIND_SPOILER:       {"<span data-mx-spoiler>", "</span>"},
}

// rendered is a body as kith keeps it: each mention's name where Signal left U+FFFC,
// its styles drawn over that text, and whether self is mentioned.
func rendered(self, body string, mentions []*signalv1.Mention, styles []*signalv1.Style) (string, richtext.Formatted, bool) {
	text, at, me := mentioned(self, body, mentions)
	var marks []richtext.Mark
	for _, s := range styles {
		start, end := int(s.GetStart()), int(s.GetStart())+int(s.GetLength())
		tag, known := tags[s.GetKind()]
		if !known || start >= end || end > len(at)-1 {
			continue // past the text: a malformed style draws nothing
		}
		marks = append(marks, richtext.Mark{Start: at[start], End: at[end], Open: tag[0], Close: tag[1]})
	}
	if len(marks) == 0 {
		return text, richtext.Formatted{}, me
	}
	f := richtext.FromMarkup(richtext.Sanitize(richtext.Nested(text, marks)))
	if f.IsZero() || f.Text() == "" {
		return text, richtext.Formatted{}, me
	}
	// The body is exactly the formatting's text.
	return f.Text(), f, me
}

// outgoing is a draft as Signal sends it: its Markdown (unless sent as typed) as styles,
// a link's address written after its words, and its mentions as Signal's.
func outgoing(draft domain.Draft) (string, []*signalv1.Mention, []*signalv1.Style) {
	var spans []richtext.Span
	if !draft.Plain {
		if markup, err := markdown.HTML(draft.Body); err == nil {
			// No formatting: send as typed.
			if f := richtext.FromMarkup(richtext.Sanitize(markup)); !f.IsZero() && f.Text() != "" {
				draft.Body, spans = addresses(f.Text(), f.Spans())
			}
		}
	}
	body, mentions, units := mentionsOf(draft)
	var styles []*signalv1.Style
	for _, s := range spans {
		if s.Empty() || s.End > len(units)-1 {
			continue
		}
		start, length := uint32(units[s.Start]), uint32(units[s.End]-units[s.Start]) //nolint:gosec // a message's length fits
		for kind, on := range map[signalv1.Style_Kind]bool{
			signalv1.Style_KIND_BOLD:          s.Bold || s.Heading,
			signalv1.Style_KIND_ITALIC:        s.Italic,
			signalv1.Style_KIND_STRIKETHROUGH: s.Strike,
			signalv1.Style_KIND_MONOSPACE:     s.Code,
			signalv1.Style_KIND_SPOILER:       s.Spoiler,
		} {
			if on {
				styles = append(styles, &signalv1.Style{Start: start, Length: length, Kind: kind})
			}
		}
	}
	slices.SortFunc(styles, func(x, y *signalv1.Style) int {
		if x.GetStart() != y.GetStart() {
			return int(x.GetStart()) - int(y.GetStart())
		}
		return int(x.GetKind()) - int(y.GetKind())
	})
	return body, mentions, styles
}

// addresses writes each link's address after its words, " (https://…)", when they
// differ: Signal has no links but finds addresses in text. Spans after it move along.
func addresses(text string, spans []richtext.Span) (string, []richtext.Span) {
	type insert struct {
		at    int
		words string
	}
	var inserts []insert
	for _, s := range spans {
		if !s.Link || s.Href == "" || s.Empty() {
			continue
		}
		if words := text[s.Start:s.End]; words != s.Href && "mailto:"+words != s.Href {
			inserts = append(inserts, insert{at: s.End, words: " (" + s.Href + ")"})
		}
	}
	if len(inserts) == 0 {
		return text, spans
	}
	slices.SortStableFunc(inserts, func(a, b insert) int { return a.at - b.at })
	// moved is where offset i of text lands: past every address written before it, and
	// past one written at it when after (a span starting there starts after it).
	moved := func(i int, after bool) int {
		n := i
		for _, in := range inserts {
			if in.at < i || (after && in.at == i) {
				n += len(in.words)
			}
		}
		return n
	}
	var out strings.Builder
	last := 0
	for _, in := range inserts {
		out.WriteString(text[last:in.at])
		out.WriteString(in.words)
		last = in.at
	}
	out.WriteString(text[last:])
	shifted := slices.Clone(spans)
	for i := range shifted {
		shifted[i].Start, shifted[i].End = moved(shifted[i].Start, true), moved(shifted[i].End, false)
	}
	return out.String(), shifted
}
