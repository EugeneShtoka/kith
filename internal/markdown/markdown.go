// Package markdown renders what is typed in kith's composer to HTML, for every network
// that sends formatting: Matrix sends the HTML, Telegram the entities it flattens to.
// One renderer, so a draft formats alike wherever it goes.
package markdown

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/format/mdext"
)

// Renderer is CommonMark (as Element's composer), not chat-app conventions (`*x*` is
// italic, `__x__` bold). No single-tilde strikethrough: it would mangle `~/path` and
// "~5". Spoilers are on (goldmark parses code before inline extensions, so `a || b`
// in code is safe). Tables are off: no network renders them. HTML in the source is
// escaped, never passed through.
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
