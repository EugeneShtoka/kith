package whatsapp

import (
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// WhatsApp's formatting is markers in the text (richtext.FromMarkers): *bold*,
// _italic_, ~strikethrough~, `code` and ```monospace``` blocks. kith keeps formatting
// as richtext markup, which they all map into; the text a message is searched by is
// the words without the markers.

// formatted is a WhatsApp text as kith keeps it: the words, and their formatting
// (zero when there is none).
func formatted(text string) (string, richtext.Formatted) {
	markup, any := richtext.FromMarkers(text)
	if !any {
		return text, richtext.Formatted{}
	}
	f := richtext.FromMarkup(richtext.Sanitize(markup))
	if f.IsZero() || f.Text() == "" {
		return text, richtext.Formatted{} // formatting around nothing: the text as typed
	}
	// The words are what the formatting draws over, exactly: the two never disagree.
	return f.Text(), f
}

// fromMarkdown is a Markdown draft in WhatsApp's markers (richtext.ToMarkers), a link
// as its words and address.
func fromMarkdown(text string) string {
	return richtext.ToMarkers(text, func(words, address string) string {
		if words == address {
			return address
		}
		return words + " (" + address + ")"
	})
}
