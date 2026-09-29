// Package richtext is the single table of Matrix HTML formatting this client
// understands. Formatted bodies are attacker-controlled, so they are sanitized once on
// the way in (Sanitize) and flattened to text plus emphasis spans for drawing (Parse).
package richtext

import (
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// allowed is the tags kept, deliberately smaller than the spec's list: only what a
// terminal can draw. Dropped tags (img, tables, font/div, details) keep their text.
var allowed = map[string]mark{
	"b": markBold, "strong": markBold,
	"i": markItalic, "em": markItalic,
	"code": markCode,
	"del":  markStrike, "s": markStrike, "strike": markStrike,
	"u": markUnderline, "ins": markUnderline,
	"a":          markLink,
	"blockquote": markQuote,
	"h1":         markHeading, "h2": markHeading, "h3": markHeading,
	"h4": markHeading, "h5": markHeading, "h6": markHeading,
	"pre": markCode,
	// Structure carries no emphasis but must survive so lists keep their bullets.
	"p": 0, "ul": 0, "ol": 0, "li": 0,
	// Only spans carrying data-mx-spoiler are kept (see openTag).
	"span": markSpoiler,
}

// silent tags are dropped together with their contents.
var silent = []string{"script", "style", "title", "textarea", "head"}

// blocks end the line.
var blocks = []string{"p", "div", "blockquote", "pre", "li", "ul", "ol",
	"h1", "h2", "h3", "h4", "h5", "h6"}

const bullet = "• "

// mark is a bitmask of emphases, since they nest.
type mark uint16

const (
	markBold mark = 1 << iota
	markItalic
	markCode
	markStrike
	markUnderline
	markLink
	markQuote
	markHeading
	markSpoiler
)

// Span is an emphasized run of Parse's text, in logical byte offsets (the only
// positions that survive bidi reordering).
type Span struct {
	Start, End int
	Bold       bool
	Italic     bool
	Code       bool
	Strike     bool
	Underline  bool
	Link       bool
	Quote      bool
	Heading    bool
	// Reason is a spoiler's optional label ("plot", "nsfw").
	Spoiler bool
	Reason  string
	// Href is the link target, set only when Link is.
	Href string
}

// Empty reports whether a span would draw nothing.
func (s Span) Empty() bool {
	return s.Start >= s.End || (!s.Bold && !s.Italic && !s.Code && !s.Strike &&
		!s.Underline && !s.Link && !s.Quote && !s.Heading && !s.Spoiler)
}

// Sanitize keeps the formatting this client understands and drops the rest; text is
// never dropped. Attributes are removed except a safe-scheme href on links and the
// spoiler attribute on spans. It returns "" when the result would draw exactly what
// the plain body does.
//
//nolint:gocognit // flat dispatch over the tokenizer's token kinds
func Sanitize(formatted string) string {
	if strings.TrimSpace(formatted) == "" {
		return ""
	}
	var out strings.Builder
	var open []string // the allowed tags currently open, so each is closed once
	quiet := ""       // the silent tag being skipped over, contents and all
	tokens := html.NewTokenizer(strings.NewReader(formatted))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			// Any error ends it, EOF included.
			for _, o := range slices.Backward(open) {
				out.WriteString("</" + o + ">")
			}
			return keptOrNothing(out.String())
		case html.TextToken:
			if quiet != "" {
				continue // inside a script or a style: its text is not a message
			}
			out.WriteString(html.EscapeString(string(tokens.Text())))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			tag := string(name)
			if quiet == "" && slices.Contains(silent, tag) {
				quiet = tag
				continue
			}
			if opened, keep := keptTag(tag, tokens); keep {
				out.WriteString(opened)
				if tag != "br" {
					open = append(open, tag)
				}
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			tag := string(name)
			if quiet != "" {
				if tag == quiet {
					quiet = ""
				}
				continue
			}
			if i := slices.Index(open, tag); i >= 0 {
				out.WriteString("</" + tag + ">")
				open = slices.Delete(open, i, i+1)
			}
		}
	}
}

const spoilerAttr = "data-mx-spoiler"

// keptTag returns the start tag to write for tag, and whether it is kept at all.
func keptTag(tag string, tokens *html.Tokenizer) (string, bool) {
	if tag == "br" {
		return "<br>", true
	}
	if _, ok := allowed[tag]; !ok {
		return "", false
	}
	opened := openTag(tag, tokens)
	return opened, opened != ""
}

// openTag writes one allowed start tag with only the attributes that survive.
func openTag(tag string, tokens *html.Tokenizer) string {
	if tag == "span" {
		reason, ok := findAttr(tokens, spoilerAttr)
		switch {
		case !ok:
			return "" // a span that is not a spoiler is a color somebody chose
		case reason == "":
			return `<span ` + spoilerAttr + `>`
		}
		return `<span ` + spoilerAttr + `="` + html.EscapeString(reason) + `">`
	}
	if tag != "a" {
		return "<" + tag + ">"
	}
	for {
		key, val, more := tokens.TagAttr()
		if string(key) == "href" && safeHref(string(val)) {
			return `<a href="` + html.EscapeString(string(val)) + `">`
		}
		if !more {
			return "<a>"
		}
	}
}

// findAttr reads the named attribute off the tag being opened.
func findAttr(tokens *html.Tokenizer, name string) (string, bool) {
	for {
		key, val, more := tokens.TagAttr()
		if string(key) == name {
			return string(val), true
		}
		if !more {
			return "", false
		}
	}
}

// safeHref allow-lists link schemes; an unknown scheme is merely unlinked.
func safeHref(href string) bool {
	for _, scheme := range []string{"https://", "http://", "mailto:", "matrix:", "ftp://", "magnet:"} {
		if strings.HasPrefix(strings.ToLower(href), scheme) {
			return true
		}
	}
	return false
}

// keptOrNothing drops a result carrying no emphasis, line break or bullet — e.g. a
// message wrapped in one <p>.
func keptOrNothing(kept string) string {
	text, spans := Parse(kept)
	if len(spans) == 0 && !strings.ContainsAny(text, "\n") && !strings.Contains(text, bullet) {
		return ""
	}
	return kept
}

// Parse flattens sanitized HTML into the text to draw and the spans to emphasize.
// Block tags become newlines and list items open with a bullet.
func Parse(formatted string) (text string, spans []Span) {
	var p parser
	tokens := html.NewTokenizer(strings.NewReader(formatted))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return p.finish()
		case html.TextToken:
			p.text(string(tokens.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokens.TagName()
			p.open(string(name), hasAttr, tokens)
		case html.EndTagToken:
			name, _ := tokens.TagName()
			p.close(string(name))
		}
	}
}

// parser is Parse's state. Open marks unwind by tag name, since incoming markup can
// nest in ways nobody wrote.
type parser struct {
	out   strings.Builder
	marks []openMark
	spans []Span
}

type openMark struct {
	mark  mark
	start int
	attr  string // link href or spoiler reason
	tag   string
}

// text writes a run of characters, dropping whitespace at the start of a line (the
// formatting between tags, e.g. "</li>\n<li>").
func (p *parser) text(s string) {
	if strings.TrimSpace(s) == "" && (p.out.Len() == 0 || strings.HasSuffix(p.out.String(), "\n")) {
		return
	}
	p.out.WriteString(s)
}

// newline ends the current line unless it already is.
func (p *parser) newline() {
	if p.out.Len() > 0 && !strings.HasSuffix(p.out.String(), "\n") {
		p.out.WriteString("\n")
	}
}

func (p *parser) open(tag string, hasAttr bool, tokens *html.Tokenizer) {
	mk, drawable := allowed[tag]
	if tag == "br" {
		p.out.WriteString("\n")
		return
	}
	if slices.Contains(blocks, tag) {
		p.newline()
	}
	if tag == "li" {
		p.out.WriteString(bullet)
	}
	if !drawable {
		return
	}
	p.marks = append(p.marks, openMark{mark: mk, start: p.out.Len(), attr: attrOf(tag, hasAttr, tokens), tag: tag})
}

// close ends a tag, emitting its span. An unmatched closing tag still breaks the
// line if it is a block.
func (p *parser) close(tag string) {
	i := slices.IndexFunc(p.marks, func(o openMark) bool { return o.tag == tag })
	if i >= 0 {
		p.emit(p.marks[i])
		p.marks = slices.Delete(p.marks, i, i+1)
	}
	if slices.Contains(blocks, tag) {
		p.newline()
	}
}

func (p *parser) emit(o openMark) {
	span := Span{Start: o.start, End: p.out.Len()}
	applyMark(&span, o.mark)
	if span.Spoiler {
		span.Reason = o.attr
	} else {
		span.Href = o.attr
	}
	if !span.Empty() {
		p.spans = append(p.spans, span)
	}
}

// finish closes everything still open and trims trailing newlines.
func (p *parser) finish() (string, []Span) {
	for _, v := range slices.Backward(p.marks) {
		p.emit(v)
	}
	return strings.TrimRight(p.out.String(), "\n"), p.spans
}

func applyMark(span *Span, mk mark) {
	span.Bold = span.Bold || mk&markBold != 0 || mk&markHeading != 0
	span.Italic = span.Italic || mk&markItalic != 0
	span.Code = span.Code || mk&markCode != 0
	span.Strike = span.Strike || mk&markStrike != 0
	span.Underline = span.Underline || mk&markUnderline != 0
	span.Link = span.Link || mk&markLink != 0
	span.Quote = span.Quote || mk&markQuote != 0
	span.Heading = span.Heading || mk&markHeading != 0
	span.Spoiler = span.Spoiler || mk&markSpoiler != 0
}

// attrOf reads the one attribute a tag may carry: a link's href or a spoiler's label.
func attrOf(tag string, hasAttr bool, tokens *html.Tokenizer) string {
	want := map[string]string{"a": "href", "span": spoilerAttr}[tag]
	if !hasAttr || want == "" {
		return ""
	}
	v, _ := findAttr(tokens, want)
	return v
}
