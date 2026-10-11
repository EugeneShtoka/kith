// Package markdown renders composer text to HTML for every network that sends
// formatting (Matrix sends the HTML, Telegram flattens it to entities).
package markdown

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
)

// Renderer is CommonMark like Element's composer (`*x*` italic, `__x__` bold). No
// single-tilde strikethrough (it mangles `~/path`); spoilers on (code is parsed first,
// so `a || b` in code is safe); no tables (no network renders them); source HTML escaped.
var Renderer = goldmark.New(
	goldmark.WithExtensions(extension.Strikethrough, mdext.EscapeHTML, mdext.Spoiler),
	format.HTMLOptions,
)

// HTML is source rendered to HTML.
func HTML(source string) (string, error) {
	var out bytes.Buffer
	if err := Renderer.Convert([]byte(source), &out); err != nil {
		return "", err //nolint:wrapcheck // goldmark's own words
	}
	return out.String(), nil
}
