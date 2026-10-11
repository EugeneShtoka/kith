package richtext

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Markers are inline formatting characters as WhatsApp and Slack (mrkdwn) write them:
// *bold*, _italic_, ~strikethrough~, `code` and ```code blocks```.

// emphasis are the inline markers and the tags they become.
var emphasis = map[byte]string{'*': "b", '_': "i", '~': "s"}

// FromMarkers turns markers into markup and reports whether any were found. Other text
// is escaped; a ```block``` is taken verbatim.
func FromMarkers(text string) (markup string, any bool) {
	var m strings.Builder
	for {
		before, after, open := strings.Cut(text, "```")
		inner, rest, closed := strings.Cut(after, "```")
		if !open || !closed || inner == "" {
			mm, a := inline(text)
			m.WriteString(mm)
			return m.String(), any || a
		}
		mm, _ := inline(before)
		// The newlines framing "```\ncode\n```" are not part of the block.
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "\n"), "\n")
		m.WriteString(mm + "<pre><code>" + html.EscapeString(inner) + "</code></pre>")
		any, text = true, rest
	}
}

// inline formats one stretch with no code block in it.
func inline(text string) (markup string, any bool) {
	var m strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c == '`' {
			if j := strings.IndexByte(text[i+1:], '`'); j > 0 && !strings.Contains(text[i+1:i+1+j], "\n") {
				m.WriteString("<code>" + html.EscapeString(text[i+1:i+1+j]) + "</code>")
				any = true
				i += j + 2
				continue
			}
		}
		if tag, ok := emphasis[c]; ok {
			if end := closing(text, i); end > 0 {
				mm, _ := inline(text[i+1 : end])
				m.WriteString("<" + tag + ">" + mm + "</" + tag + ">")
				any = true
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		m.WriteString(html.EscapeString(string(r)))
		i += size
	}
	return m.String(), any
}

// closing returns where the marker at open closes, or 0. A marker opens after a
// boundary before a non-space and closes after a non-space before a boundary, on one line.
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

// The composer writes Markdown; for marker networks the common forms translate one to
// one and anything else goes as typed.
var (
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdItalic  = regexp.MustCompile(`(^|[^\pL\pN*])\*([^*\s][^*\n]*?)\*([^\pL\pN*]|$)`)
	mdStrike  = regexp.MustCompile(`~~([^~\n]+)~~`)
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	codeSpans = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")
)

// ToMarkers converts a Markdown draft to markers (**b**/__b__ to *b*, *i* to _i_,
// ~~s~~ to ~s~, links via link); code is left as is.
func ToMarkers(text string, link func(words, address string) string) string {
	var out strings.Builder
	last := 0
	for _, span := range codeSpans.FindAllStringIndex(text, -1) {
		out.WriteString(prose(text[last:span[0]], link))
		out.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	out.WriteString(prose(text[last:], link))
	return out.String()
}

// prose translates the Markdown in text that holds no code.
func prose(text string, link func(words, address string) string) string {
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
		return link(m[1], m[2])
	})
}
