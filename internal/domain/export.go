package domain

import (
	"strconv"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// A conversation is exported as Markdown a person can read in any editor, or keep:
// a heading per day; each message its time and sender, what it replies to, its
// words with their formatting, its attachment, and its reactions; a thread's replies
// gathered under the message that began it. People are named as kith names them,
// whole; times and dates as the person has them written.

// ExportOf is what an export is of.
type ExportOf struct {
	Title   string    // the room's name, or the thread's
	Network string    // "Telegram", "WhatsApp", …
	Span    string    // the span asked for, as typed ("7d"), "" for all of it
	At      time.Time // when it was exported
}

// quoteRunes is how much of a replied-to message an export quotes.
const quoteRunes = 80

// ExportMarkdown is msgs, oldest first, as Markdown. name names a person from their ID
// and the names known for them (People.Name); reactions are the room's.
func ExportMarkdown(of ExportOf, msgs []Message, reactions []Reaction, name func(userID string, known ...string) string, clock Clock) string {
	byID := make(map[EventID]*Message, len(msgs))
	for i := range msgs {
		byID[msgs[i].ID] = &msgs[i]
	}
	replies := map[EventID][]*Message{} // a thread's replies, under a root the export holds
	for i := range msgs {
		if root := msgs[i].ThreadRoot; root != "" && root != msgs[i].ID && byID[root] != nil {
			replies[root] = append(replies[root], &msgs[i])
		}
	}
	reacted := map[EventID][]Reaction{}
	for _, r := range reactions {
		reacted[r.Target] = append(reacted[r.Target], r)
	}
	w := exportWriter{byID: byID, reacted: reacted, name: name, clock: clock}

	var b strings.Builder
	b.WriteString("# " + richtext.EscapeMarkdown(of.Title) + "\n\n")
	about := []string{}
	if of.Network != "" {
		about = append(about, of.Network)
	}
	about = append(about, "exported "+clock.ShortDate(of.At)+" "+clock.Time(of.At), messagesCount(len(msgs)))
	if of.Span != "" {
		about = append(about, "since "+richtext.EscapeMarkdown(of.Span))
	}
	b.WriteString(strings.Join(about, " · ") + "\n")

	day := ""
	for i := range msgs {
		m := &msgs[i]
		if root := m.ThreadRoot; root != "" && root != m.ID && byID[root] != nil {
			continue // written under its root
		}
		if d := clock.LongDate(m.Timestamp); d != day {
			day = d
			b.WriteString("\n## " + richtext.EscapeMarkdown(d) + "\n")
		}
		b.WriteString("\n" + w.message(m, false))
		if thread := replies[m.ID]; len(thread) > 0 {
			b.WriteString(">\n> **Thread · " + repliesCount(len(thread)) + "**\n")
			for _, r := range thread {
				b.WriteString(">\n" + quoteLines(w.message(r, true)))
			}
		}
	}
	return b.String()
}

// exportWriter writes one message at a time.
type exportWriter struct {
	byID    map[EventID]*Message
	reacted map[EventID][]Reaction
	name    func(userID string, known ...string) string
	clock   Clock
}

// message is one message's block: a line naming its time and sender, then its words.
func (w exportWriter) message(m *Message, inThread bool) string {
	var b strings.Builder
	head := "**" + w.clock.Time(m.Timestamp) + " · " + richtext.EscapeMarkdown(w.name(m.Sender, m.SenderName)) + "**"
	if inThread {
		head += " · " + w.clock.ShortDate(m.Timestamp)
	}
	if m.Edited && !m.Redacted {
		head += " _(edited)_"
	}
	if m.ThreadRoot != "" && m.ThreadRoot != m.ID && !inThread && w.byID[m.ThreadRoot] == nil {
		head += " _(in a thread)_"
	}
	b.WriteString(head + "  \n")
	if m.ReplyTo != "" {
		b.WriteString(w.quote(m.ReplyTo))
	}
	if m.Redacted {
		b.WriteString("_(deleted)_\n")
		return b.String()
	}
	if words := w.words(m); words != "" {
		b.WriteString(words + "\n")
	}
	if md := m.Media; md != nil {
		b.WriteString(attachmentLine(md) + "\n")
	}
	if chips := AggregateReactions(w.reacted[m.ID], ""); len(chips) > 0 {
		parts := make([]string, len(chips))
		for i, c := range chips {
			parts[i] = c.Key + " " + strconv.Itoa(c.Count)
		}
		b.WriteString(strings.Join(parts, " · ") + "\n")
	}
	return b.String()
}

// words is a message's words as Markdown, its formatting kept and the people it
// mentions named now. A file's own name is no words: the attachment line says it.
func (w exportWriter) words(m *Message) string {
	text, spans := m.Body, []richtext.Span(nil)
	if !m.Format.IsZero() {
		text, spans = m.Format.Text(), m.Format.Spans()
	}
	if m.Media != nil && text == m.Media.Name {
		return ""
	}
	md := richtext.Markdown(text, spans)
	// Mentions are found as Markdown wrote their words, and named in Markdown.
	escaped := make([]Mention, len(m.Mentions))
	for i, mn := range m.Mentions {
		mn.Name = richtext.EscapeMarkdown(mn.Name)
		escaped[i] = mn
	}
	md, _ = ResolveMentions(md, escaped, func(mn Mention, words string) string {
		return richtext.EscapeMarkdown(w.name(mn.UserID, mn.Known, words))
	})
	// Two spaces end a line in Markdown, so a message's lines stay its lines.
	return strings.ReplaceAll(md, "\n", "  \n")
}

// quote is the line quoting what a message replies to: its sender and first words.
func (w exportWriter) quote(target EventID) string {
	quoted := w.byID[target]
	if quoted == nil {
		return "> ↳ _a message not in this export_\n"
	}
	words := quoted.Body
	if quoted.Redacted {
		words = "(deleted)"
	}
	words, _, _ = strings.Cut(strings.TrimSpace(words), "\n")
	if r := []rune(words); len(r) > quoteRunes {
		words = string(r[:quoteRunes]) + "…"
	}
	return "> ↳ **" + richtext.EscapeMarkdown(w.name(quoted.Sender, quoted.SenderName)) + "**: " + richtext.EscapeMarkdown(words) + "\n"
}

// attachmentLine names an attachment: its kind, name and size.
func attachmentLine(md *Media) string {
	parts := []string{"📎 " + string(md.Type)}
	if md.Name != "" {
		parts = append(parts, "_"+richtext.EscapeMarkdown(md.Name)+"_")
	}
	if md.Size > 0 {
		parts = append(parts, HumanSize(md.Size))
	}
	return strings.Join(parts, " · ")
}

// quoteLines puts a block inside a blockquote.
func quoteLines(block string) string {
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	for i := range lines {
		lines[i] = "> " + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func messagesCount(n int) string {
	if n == 1 {
		return "1 message"
	}
	return strconv.Itoa(n) + " messages"
}

func repliesCount(n int) string {
	if n == 1 {
		return "1 reply"
	}
	return strconv.Itoa(n) + " replies"
}
