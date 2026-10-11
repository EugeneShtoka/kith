package slack

import (
	"strconv"
	"strings"
	"unicode"

	slackgo "github.com/slack-go/slack"
)

// Many apps and bots post only blocks, and the web client sends rich text beside a
// person's text. A message with no text reads from its blocks, converted to mrkdwn so
// mentions, links and formatting go through render.

// blockText is a message's blocks as mrkdwn; "" when nothing is showable (an image, a
// button).
func blockText(blocks slackgo.Blocks) string {
	var parts []string
	for _, b := range blocks.BlockSet {
		if s := strings.TrimSpace(oneBlock(b)); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func oneBlock(b slackgo.Block) string {
	switch b := b.(type) {
	case *slackgo.RichTextBlock:
		var out []string
		for _, e := range b.Elements {
			out = append(out, richElement(e))
		}
		return strings.Join(out, "\n")
	case *slackgo.SectionBlock:
		var out []string
		if b.Text != nil {
			out = append(out, textObject(b.Text))
		}
		for _, f := range b.Fields {
			out = append(out, textObject(f))
		}
		return strings.Join(out, "\n")
	case *slackgo.HeaderBlock:
		if b.Text != nil {
			return "*" + textObject(b.Text) + "*"
		}
	case *slackgo.ContextBlock:
		var out []string
		for _, e := range b.ContextElements.Elements {
			if t, ok := e.(*slackgo.TextBlockObject); ok {
				out = append(out, textObject(t))
			}
		}
		return strings.Join(out, " · ")
	}
	return ""
}

// textObject is a text object as mrkdwn; plain text is escaped so "<" is no reference.
func textObject(t *slackgo.TextBlockObject) string {
	if t.Type == slackgo.PlainTextType {
		return escape.Replace(t.Text)
	}
	return t.Text
}

// richElement converts one rich-text element: paragraph, list, quote or code block.
func richElement(e slackgo.RichTextElement) string {
	switch e := e.(type) {
	case *slackgo.RichTextSection:
		return richRun(e.Elements)
	case *slackgo.RichTextList:
		var items []string
		for i, item := range e.Elements {
			lead := strings.Repeat("  ", e.Indent) + "• "
			if e.Style == slackgo.RTEListOrdered {
				lead = strings.Repeat("  ", e.Indent) + strconv.Itoa(e.Offset+i+1) + ". "
			}
			items = append(items, lead+richElement(item))
		}
		return strings.Join(items, "\n")
	case *slackgo.RichTextQuote:
		lines := strings.Split(richRun(e.Elements), "\n")
		for i, l := range lines {
			lines[i] = "&gt; " + l
		}
		return strings.Join(lines, "\n")
	case *slackgo.RichTextPreformatted:
		return "```" + richRun(e.Elements) + "```"
	}
	return ""
}

// richRun converts a run of rich-text pieces to mrkdwn.
func richRun(elements []slackgo.RichTextSectionElement) string {
	var b strings.Builder
	for _, e := range elements {
		switch e := e.(type) {
		case *slackgo.RichTextSectionTextElement:
			b.WriteString(styled(escape.Replace(e.Text), e.Style))
		case *slackgo.RichTextSectionLinkElement:
			link := "<" + e.URL + ">"
			if e.Text != "" && e.Text != e.URL {
				link = "<" + e.URL + "|" + escape.Replace(e.Text) + ">"
			}
			b.WriteString(styled(link, e.Style))
		case *slackgo.RichTextSectionUserElement:
			b.WriteString(styled("<@"+e.UserID+">", e.Style))
		case *slackgo.RichTextSectionChannelElement:
			b.WriteString(styled("<#"+e.ChannelID+">", e.Style))
		case *slackgo.RichTextSectionUserGroupElement:
			b.WriteString("<!subteam^" + e.UsergroupID + ">")
		case *slackgo.RichTextSectionBroadcastElement:
			b.WriteString("<!" + string(e.Range) + ">")
		case *slackgo.RichTextSectionEmojiElement:
			b.WriteString(emojiOf(e))
		case *slackgo.RichTextSectionDateElement:
			if e.Fallback != nil {
				b.WriteString(escape.Replace(*e.Fallback))
			}
		}
	}
	return b.String()
}

// styled wraps the words, not surrounding spaces, in their style's markers: a marker
// next to a space does not open or close in mrkdwn.
func styled(text string, style *slackgo.RichTextSectionTextStyle) string {
	if style == nil || strings.TrimSpace(text) == "" {
		return text
	}
	lead := text[:len(text)-len(strings.TrimLeft(text, " "))]
	trail := text[len(strings.TrimRight(text, " ")):]
	core := strings.TrimSpace(text)
	if style.Code {
		core = "`" + core + "`"
	}
	if style.Bold {
		core = "*" + core + "*"
	}
	if style.Italic {
		core = "_" + core + "_"
	}
	if style.Strike {
		core = "~" + core + "~"
	}
	return lead + core + trail
}

// emojiOf is an emoji from Slack's code points ("1f44d" or "1f469-200d-1f4bb"), else
// :name:.
func emojiOf(e *slackgo.RichTextSectionEmojiElement) string {
	if e.Unicode == "" {
		return ":" + e.Name + ":"
	}
	var b strings.Builder
	for cp := range strings.SplitSeq(e.Unicode, "-") {
		r, err := strconv.ParseUint(cp, 16, 21)
		if err != nil || r > unicode.MaxRune {
			return ":" + e.Name + ":"
		}
		b.WriteRune(rune(r))
	}
	return b.String()
}
