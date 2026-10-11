package richtext

import "slices"

// Formatted is a message's network-neutral formatting. It is stored as kith's markup
// (Sanitize's HTML subset, which every network's formatting converts into losslessly)
// and drawn as Parse's text and spans.
//
// Immutable and built only by FromMarkup or Drawn, so the two forms never disagree.
// The zero value is no formatting.
type Formatted struct {
	markup string
	text   string
	spans  []Span
}

// FromMarkup returns the formatting for already-sanitized markup; "" is none.
func FromMarkup(markup string) Formatted {
	if markup == "" {
		return Formatted{}
	}
	text, spans := Parse(markup)
	return Formatted{markup: markup, text: text, spans: spans}
}

// Drawn is formatting with no markup, as a client receives it; it cannot be stored.
func Drawn(text string, spans []Span) Formatted {
	if text == "" && len(spans) == 0 {
		return Formatted{}
	}
	return Formatted{text: text, spans: slices.Clone(spans)}
}

// IsZero reports whether there is no formatting.
func (f Formatted) IsZero() bool { return f.markup == "" && f.text == "" && len(f.spans) == 0 }

// Markup is the stored form, "" when the formatting is only Drawn.
func (f Formatted) Markup() string { return f.markup }

// Text is the text the formatting draws.
func (f Formatted) Text() string { return f.text }

// Spans are the emphasized runs of Text, in byte offsets. The slice is a copy.
func (f Formatted) Spans() []Span { return slices.Clone(f.spans) }
