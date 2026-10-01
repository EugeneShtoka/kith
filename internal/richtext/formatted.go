package richtext

import "slices"

// Formatted is a message's formatting, network-neutral. Its stored form is kith's
// markup: the sanitized HTML subset this package keeps (Sanitize), which every network's
// formatting converts into without loss (Matrix is already HTML; WhatsApp's and
// Telegram's marks are a subset of it). Its drawn form is the text and spans that markup
// flattens to (Parse).
//
// It is immutable and built only by FromMarkup or Drawn, so the drawn form can never
// disagree with the stored one. The zero value is no formatting: the plain body draws.
type Formatted struct {
	markup string
	text   string
	spans  []Span
}

// FromMarkup is the formatting kith's markup describes. markup must already be
// sanitized (Sanitize's output, or what the cache stored from it); "" is no formatting.
func FromMarkup(markup string) Formatted {
	if markup == "" {
		return Formatted{}
	}
	text, spans := Parse(markup)
	return Formatted{markup: markup, text: text, spans: spans}
}

// Drawn is formatting known only by how it draws, as a client receives it: it has no
// markup, so it cannot be stored.
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
