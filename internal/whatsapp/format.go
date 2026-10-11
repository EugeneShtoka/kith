package whatsapp

import (
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// WhatsApp formats with in-text markers: *bold*, _italic_, ~strikethrough~, `code`,
// ```monospace```. kith stores richtext; searchable text omits the markers.

// formatted splits WhatsApp text into plain text and formatting (zero when none).
func formatted(text string) (string, richtext.Formatted) {
	markup, any := richtext.FromMarkers(text)
	if !any {
		return text, richtext.Formatted{}
	}
	f := richtext.FromMarkup(richtext.Sanitize(markup))
	if f.IsZero() || f.Text() == "" {
		return text, richtext.Formatted{} // markers around nothing: keep as typed
	}
	return f.Text(), f
}

// fromMarkdown converts Markdown to WhatsApp markers; a link becomes text and URL.
func fromMarkdown(text string) string {
	return richtext.ToMarkers(text, func(words, address string) string {
		if words == address {
			return address
		}
		return words + " (" + address + ")"
	})
}
