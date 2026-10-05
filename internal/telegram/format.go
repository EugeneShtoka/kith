package telegram

import (
	"html"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
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

// mediaLabel is what a message's attachment reads as until media come: its kind, and
// a file's name.
func mediaLabel(media tg.MessageMediaClass) string {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		return "[photo]"
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "[file]"
		}
		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					return "[voice message]"
				}
				return "[audio]"
			case *tg.DocumentAttributeVideo:
				if a.RoundMessage {
					return "[video message]"
				}
				return "[video]"
			case *tg.DocumentAttributeSticker:
				return "[sticker] " + a.Alt
			case *tg.DocumentAttributeFilename:
				return "[file] " + a.FileName
			}
		}
		return "[file]"
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "[location]"
	case *tg.MessageMediaContact:
		return "[contact] " + strings.TrimSpace(m.FirstName+" "+m.LastName)
	case *tg.MessageMediaPoll:
		return "[poll] " + m.Poll.Question.Text
	case *tg.MessageMediaWebPage:
		return "" // a link preview: the text has the link
	case *tg.MessageMediaDice:
		return "[dice] " + m.Emoticon + " " + strconv.Itoa(m.Value)
	}
	return "[attachment]"
}
