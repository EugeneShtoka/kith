package whatsapp

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// WhatsApp's formatting is markers in the text: *bold*, _italic_, ~strikethrough~,
// `code` and ```monospace``` blocks. kith keeps formatting as richtext markup, which
// they all map into; the text a message is searched by is the words without the
// markers.

// emphasis are the inline markers and the tags they become.
var emphasis = map[byte]string{'*': "b", '_': "i", '~': "s"}

// formatted is a WhatsApp text as kith keeps it: the words, and their formatting
// (zero when there is none).
func formatted(text string) (string, richtext.Formatted) {
	markup, _, any := toMarkup(text)
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

// toMarkup turns WhatsApp markers into markup, and reports the words alone and
// whether there was any formatting at all. A ```block``` is taken as it is written.
func toMarkup(text string) (markup, plain string, any bool) {
	var m, p strings.Builder
	for {
		before, after, open := strings.Cut(text, "```")
		inner, rest, closed := strings.Cut(after, "```")
		if !open || !closed || inner == "" {
			mm, pp, a := inline(text)
			m.WriteString(mm)
			p.WriteString(pp)
			return m.String(), p.String(), any || a
		}
		mm, pp, _ := inline(before)
		// "```\ncode\n```" is how a block is typed: the newlines frame it, they are not in it.
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "\n"), "\n")
		m.WriteString(mm + "<pre><code>" + html.EscapeString(inner) + "</code></pre>")
		p.WriteString(pp + inner)
		any, text = true, rest
	}
}

// inline formats one stretch with no code block in it.
func inline(text string) (markup, plain string, any bool) {
	var m, p strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c == '`' {
			if j := strings.IndexByte(text[i+1:], '`'); j > 0 && !strings.Contains(text[i+1:i+1+j], "\n") {
				code := text[i+1 : i+1+j]
				m.WriteString("<code>" + html.EscapeString(code) + "</code>")
				p.WriteString(code)
				any = true
				i += j + 2
				continue
			}
		}
		if tag, ok := emphasis[c]; ok {
			if end := closing(text, i); end > 0 {
				mm, pp, _ := inline(text[i+1 : end])
				m.WriteString("<" + tag + ">" + mm + "</" + tag + ">")
				p.WriteString(pp)
				any = true
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		m.WriteString(html.EscapeString(string(r)))
		p.WriteRune(r)
		i += size
	}
	return m.String(), p.String(), any
}

// closing is where the marker at open closes, or 0: as WhatsApp reads it, a marker
// opens after a boundary (start, space, punctuation) before a non-space, and closes
// after a non-space before a boundary, on the same line.
func closing(text string, open int) int {
	marker := text[open]
	if open > 0 {
		before, _ := utf8.DecodeLastRuneInString(text[:open])
		if isWordRune(before) {
			return 0
		}
	}
	if open+1 >= len(text) || isSpaceByte(text[open+1]) {
		return 0
	}
	for j := open + 2; j < len(text); j++ {
		switch text[j] {
		case '\n':
			return 0
		case marker:
			if isSpaceByte(text[j-1]) {
				continue
			}
			if j+1 < len(text) {
				after, _ := utf8.DecodeRuneInString(text[j+1:])
				if isWordRune(after) {
					continue
				}
			}
			return j
		}
	}
	return 0
}

func isWordRune(r rune) bool  { return unicode.IsLetter(r) || unicode.IsDigit(r) }
func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' }

// Kith's composer writes Markdown; WhatsApp reads its own markers. The common forms
// translate one to one; anything else goes as typed.
var (
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdItalic  = regexp.MustCompile(`(^|[^\pL\pN*])\*([^*\s][^*\n]*?)\*([^\pL\pN*]|$)`)
	mdStrike  = regexp.MustCompile(`~~([^~\n]+)~~`)
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	codeSpans = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")
)

// fromMarkdown is a Markdown draft in WhatsApp's markers: **bold** and __bold__ as
// *bold*, *italic* as _italic_, ~~strike~~ as ~strike~, a link as its words and
// address. Code is left as it is.
func fromMarkdown(text string) string {
	var out strings.Builder
	last := 0
	for _, span := range codeSpans.FindAllStringIndex(text, -1) {
		out.WriteString(prose(text[last:span[0]]))
		out.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	out.WriteString(prose(text[last:]))
	return out.String()
}

// prose translates the markers in text that holds no code.
func prose(text string) string {
	// Bold first, to a placeholder, so its stars are not read as italic's.
	text = mdBold.ReplaceAllStringFunc(text, func(s string) string {
		inner := mdBold.FindStringSubmatch(s)
		return "\x00" + inner[1] + inner[2] + "\x00"
	})
	text = mdItalic.ReplaceAllString(text, "${1}_${2}_${3}")
	text = strings.ReplaceAll(text, "\x00", "*")
	text = mdStrike.ReplaceAllString(text, "~${1}~")
	return mdLink.ReplaceAllStringFunc(text, func(s string) string {
		m := mdLink.FindStringSubmatch(s)
		if m[1] == m[2] {
			return m[2]
		}
		return m[1] + " (" + m[2] + ")"
	})
}
