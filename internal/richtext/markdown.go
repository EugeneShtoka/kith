package richtext

import (
	"slices"
	"strings"
)

// Markdown is text with spans written as Markdown, for a file a person reads in any
// editor: bold as **…**, italic as _…_, strikethrough as ~~…~~, code in backticks,
// a link as [text](href), a spoiler as ||…||, a quote's lines after "> ", a heading's
// after "## ". Underline has no Markdown and is written as its words. What is not
// formatting is escaped, so a "*" someone typed stays a "*".
func Markdown(text string, spans []Span) string {
	cuts := []int{0, len(text)}
	for _, s := range spans {
		if s.Start >= 0 && s.End <= len(text) && s.Start < s.End {
			cuts = append(cuts, s.Start, s.End)
		}
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)

	var b strings.Builder
	for i := 0; i+1 < len(cuts); i++ {
		from, to := cuts[i], cuts[i+1]
		var on Span
		for _, s := range spans {
			if s.Start <= from && to <= s.End {
				on = merged(on, s)
			}
		}
		b.WriteString(piece(text[from:to], on))
	}
	return lineMarks(b.String(), text, spans)
}

// merged is the emphasis of two spans covering one piece.
func merged(a, b Span) Span {
	a.Bold, a.Italic, a.Code, a.Strike = a.Bold || b.Bold, a.Italic || b.Italic, a.Code || b.Code, a.Strike || b.Strike
	a.Spoiler, a.Quote, a.Heading = a.Spoiler || b.Spoiler, a.Quote || b.Quote, a.Heading || b.Heading
	if b.Link {
		a.Link, a.Href = true, b.Href
	}
	return a
}

// piece is one run of words with the emphasis on all of it.
func piece(words string, on Span) string {
	if on.Code {
		fence := "`"
		if strings.Contains(words, "\n") {
			return "```\n" + words + "\n```"
		}
		if strings.Contains(words, "`") {
			fence = "``"
		}
		words = fence + words + fence
	} else {
		words = EscapeMarkdown(words)
	}
	if on.Link && on.Href != "" {
		words = "[" + words + "](" + strings.ReplaceAll(on.Href, ")", "%29") + ")"
	}
	wrap := func(mark string) { words = mark + words + mark }
	if on.Strike {
		wrap("~~")
	}
	if on.Italic {
		wrap("_")
	}
	if on.Bold {
		wrap("**")
	}
	if on.Spoiler {
		wrap("||")
	}
	return words
}

// lineMarks puts a quote's and a heading's marks before their lines. They are set by
// line: a line any of whose words is quoted is a quote.
func lineMarks(out, text string, spans []Span) string {
	quoted, headed := linesWith(text, spans, func(s Span) bool { return s.Quote }), linesWith(text, spans, func(s Span) bool { return s.Heading })
	if len(quoted) == 0 && len(headed) == 0 {
		return out
	}
	lines := strings.Split(out, "\n")
	for i := range lines {
		if headed[i] {
			lines[i] = "## " + lines[i]
		}
		if quoted[i] {
			lines[i] = "> " + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// linesWith is the lines of text (by number) a span is matches touches.
func linesWith(text string, spans []Span, is func(Span) bool) map[int]bool {
	out := map[int]bool{}
	for _, s := range spans {
		if !is(s) || s.Start >= s.End || s.End > len(text) {
			continue
		}
		first := strings.Count(text[:s.Start], "\n")
		last := first + strings.Count(text[s.Start:s.End], "\n")
		for line := first; line <= last; line++ {
			out[line] = true
		}
	}
	return out
}

// markdownSpecial is what Markdown would read as formatting in plain words.
var markdownSpecial = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "~", `\~`, "[", `\[`, "]", `\]`, "|", `\|`, "<", `\<`,
)

// EscapeMarkdown is words Markdown reads as written, a line's leading "#" or ">"
// included.
func EscapeMarkdown(words string) string {
	words = markdownSpecial.Replace(words)
	lines := strings.Split(words, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ">") {
			lines[i] = `\` + line
		}
	}
	return strings.Join(lines, "\n")
}
