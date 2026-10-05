package telegram

import (
	"html"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/markdown"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Telegram's formatting is entities over the text — bold, italic, underline, strike,
// code, pre, links, spoilers, quotes, mentions — at offsets counted in UTF-16 code
// units. kith keeps formatting as richtext markup, which they all map into; entities
// may overlap, and the markup nests, so one that crosses another's end is split.

// mark is one entity as markup: where it runs, in byte offsets of the text, and the
// tags around it.
type mark struct {
	start, end  int
	open, close string
}

// formatted is a Telegram text as kith keeps it: the words, their formatting (zero
// when there is none), and the people it mentions by name.
func formatted(text string, entities []tg.MessageEntityClass) (string, richtext.Formatted, []domain.Mention) {
	if len(entities) == 0 {
		return text, richtext.Formatted{}, nil
	}
	at := byteOffsets(text)
	var marks []mark
	var mentions []domain.Mention
	for _, e := range entities {
		start, end := e.GetOffset(), e.GetOffset()+e.GetLength()
		if start < 0 || end > len(at)-1 || start >= end {
			continue // past the text: a malformed entity draws nothing
		}
		m := mark{start: at[start], end: at[end]}
		if mention, ok := e.(*tg.MessageEntityMentionName); ok {
			mentions = append(mentions, domain.Mention{UserID: personID(mention.UserID), Name: text[m.start:m.end]})
			continue
		}
		if m.open, m.close = tags(e, text[m.start:m.end]); m.open != "" {
			marks = append(marks, m)
		}
	}
	if len(marks) == 0 {
		return text, richtext.Formatted{}, mentions
	}
	f := richtext.FromMarkup(richtext.Sanitize(nested(text, marks)))
	if f.IsZero() || f.Text() == "" {
		return text, richtext.Formatted{}, mentions
	}
	// The words are what the formatting draws over, exactly: the two never disagree.
	return f.Text(), f, mentions
}

// tags is the markup an entity draws as, over words; "" for one kith draws as written
// (hashtags, @usernames, custom emoji: their fallback is the text).
func tags(e tg.MessageEntityClass, words string) (open, closing string) {
	switch e := e.(type) {
	case *tg.MessageEntityBold:
		return "<b>", "</b>"
	case *tg.MessageEntityItalic:
		return "<i>", "</i>"
	case *tg.MessageEntityUnderline:
		return "<u>", "</u>"
	case *tg.MessageEntityStrike:
		return "<del>", "</del>"
	case *tg.MessageEntityCode:
		return "<code>", "</code>"
	case *tg.MessageEntityPre:
		return "<pre><code>", "</code></pre>"
	case *tg.MessageEntitySpoiler:
		return `<span data-mx-spoiler>`, "</span>"
	case *tg.MessageEntityBlockquote:
		return "<blockquote>", "</blockquote>"
	case *tg.MessageEntityTextURL:
		return `<a href="` + html.EscapeString(e.URL) + `">`, "</a>"
	case *tg.MessageEntityURL:
		return `<a href="` + html.EscapeString(words) + `">`, "</a>"
	}
	return "", ""
}

// byteOffsets maps each UTF-16 offset into text (and one past its end) to its byte
// offset. An offset inside a surrogate pair maps to the rune's start.
func byteOffsets(text string) []int {
	at := make([]int, 0, len(text)+1)
	for i, r := range text {
		for range utf16.RuneLen(r) {
			at = append(at, i)
		}
	}
	return append(at, len(text))
}

// nested is text with marks as properly nested markup: at each boundary the marks
// ending there close (and any closed on the way, still running, open again), then the
// marks starting there open, the longest outermost.
func nested(text string, marks []mark) string {
	bounds := []int{0, len(text)}
	for _, m := range marks {
		bounds = append(bounds, m.start, m.end)
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)
	var out strings.Builder
	var open []mark
	for i, at := range bounds {
		// Close what ends here; what was closed on the way opens again after.
		if lowest := slices.IndexFunc(open, func(m mark) bool { return m.end == at }); lowest >= 0 {
			var reopen []mark
			for len(open) > lowest {
				top := open[len(open)-1]
				open = open[:len(open)-1]
				out.WriteString(top.close)
				if top.end != at {
					reopen = append(reopen, top)
				}
			}
			for _, m := range slices.Backward(reopen) {
				out.WriteString(m.open)
				open = append(open, m)
			}
		}
		starting := slices.DeleteFunc(slices.Clone(marks), func(m mark) bool { return m.start != at })
		slices.SortStableFunc(starting, func(a, b mark) int { return b.end - a.end })
		for _, m := range starting {
			out.WriteString(m.open)
			open = append(open, m)
		}
		if i+1 < len(bounds) {
			out.WriteString(html.EscapeString(text[at:bounds[i+1]]))
		}
	}
	for _, m := range slices.Backward(open) {
		out.WriteString(m.close)
	}
	return out.String()
}

// outgoing is a draft as Telegram sends it: the text, its entities, and the
// formatting kith shows it with. Markdown is rendered (unless the draft is plain) and
// flattened to text and spans, each span an entity at UTF-16 offsets; each person the
// draft names (by the name typed, longest first, first unclaimed occurrence) is a
// name entity, when their access hash is known (else their name stays text).
func outgoing(draft domain.Draft, hash func(user int64) (int64, bool)) (string, []tg.MessageEntityClass, richtext.Formatted) {
	text, format := draft.Body, richtext.Formatted{}
	if !draft.Plain {
		if rendered, err := markdown.HTML(draft.Body); err == nil {
			// No formatting: the body goes as typed, as Matrix's does.
			if f := richtext.FromMarkup(richtext.Sanitize(rendered)); !f.IsZero() && f.Text() != "" {
				text, format = f.Text(), f
			}
		}
	}
	units := utf16Units(text)
	var entities []tg.MessageEntityClass
	for _, s := range format.Spans() {
		entities = append(entities, spanEntities(s, units)...)
	}
	claimed := make([]bool, len(text))
	for _, m := range longestFirst(draft.LiveMentions()) {
		user, ok := personUser(m.UserID)
		if !ok {
			continue
		}
		accessHash, ok := hash(user)
		if !ok {
			continue
		}
		at := unclaimed(text, m.Name, claimed)
		if at < 0 {
			continue
		}
		for i := at; i < at+len(m.Name); i++ {
			claimed[i] = true
		}
		entities = append(entities, &tg.InputMessageEntityMentionName{
			Offset: units[at], Length: units[at+len(m.Name)] - units[at],
			UserID: &tg.InputUser{UserID: user, AccessHash: accessHash},
		})
	}
	return text, entities, format
}

// utf16Units maps each byte offset of text (and one past its end) to its UTF-16
// offset, which is what Telegram counts in.
func utf16Units(text string) []int {
	units := make([]int, len(text)+1)
	n := 0
	for i, r := range text {
		units[i] = n
		n += utf16.RuneLen(r)
		for j := i + 1; j < i+utf8.RuneLen(r) && j < len(text); j++ {
			units[j] = n
		}
	}
	units[len(text)] = n
	return units
}

// spanEntities is a span as Telegram's entities, one per emphasis it carries.
func spanEntities(s richtext.Span, units []int) []tg.MessageEntityClass {
	if s.Empty() || s.End > len(units)-1 {
		return nil
	}
	off, n := units[s.Start], units[s.End]-units[s.Start]
	var out []tg.MessageEntityClass
	if s.Bold || s.Heading {
		out = append(out, &tg.MessageEntityBold{Offset: off, Length: n})
	}
	if s.Italic {
		out = append(out, &tg.MessageEntityItalic{Offset: off, Length: n})
	}
	if s.Underline {
		out = append(out, &tg.MessageEntityUnderline{Offset: off, Length: n})
	}
	if s.Strike {
		out = append(out, &tg.MessageEntityStrike{Offset: off, Length: n})
	}
	if s.Code {
		out = append(out, &tg.MessageEntityCode{Offset: off, Length: n})
	}
	if s.Spoiler {
		out = append(out, &tg.MessageEntitySpoiler{Offset: off, Length: n})
	}
	if s.Quote {
		out = append(out, &tg.MessageEntityBlockquote{Offset: off, Length: n})
	}
	if s.Link && s.Href != "" {
		out = append(out, &tg.MessageEntityTextURL{Offset: off, Length: n, URL: s.Href})
	}
	return out
}

// longestFirst orders mentions longest name first, so "Dan" cannot claim the "Dan"
// inside "Daniel".
func longestFirst(mentions []domain.Mention) []domain.Mention {
	ordered := slices.Clone(mentions)
	slices.SortStableFunc(ordered, func(a, b domain.Mention) int { return len(b.Name) - len(a.Name) })
	return ordered
}

// unclaimed is the first occurrence of name in text no earlier mention claimed; -1
// when there is none.
func unclaimed(text, name string, claimed []bool) int {
	if name == "" {
		return -1
	}
	for from := 0; from <= len(text)-len(name); {
		at := strings.Index(text[from:], name)
		if at < 0 {
			return -1
		}
		at += from
		if !slices.Contains(claimed[at:at+len(name)], true) {
			return at
		}
		from = at + 1
	}
	return -1
}

// personUser is the Telegram user behind a person ID.
func personUser(person string) (int64, bool) {
	id := domain.ParseID(person)
	if id.Network != domain.ProtocolTelegram || id.Account != "" {
		return 0, false
	}
	user, err := strconv.ParseInt(id.Native, 10, 64)
	return user, err == nil && user > 0
}
