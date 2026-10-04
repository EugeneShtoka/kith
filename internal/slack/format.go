package slack

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// A Slack message's text is mrkdwn: the same markers WhatsApp writes (*bold*,
// _italic_, ~strike~, `code`, ```blocks```), with &, < and > escaped, and everything
// that is not words in angle brackets — <@U…> a person, <#C…|name> a channel,
// <!here> the channel's people, <https://…|words> a link.

// names is what a workspace knows to write a message's references as words.
type names struct {
	team string
	// me is the person's own user ID: a mention of them, or of everyone, is theirs.
	me string
	// user and channel name a user and a conversation by ID, "" when unknown.
	user    func(id string) string
	channel func(id string) string
}

// rendered is a message's text as kith keeps it.
type rendered struct {
	body      string
	format    richtext.Formatted
	mentions  []domain.Mention
	mentioned bool
}

// reference is one <…> in a message's text: its words, the link it is (markup, ""
// when only words), and whom it mentions.
type reference struct {
	words, link string
	mention     *domain.Mention
	mentionsMe  bool
}

// references finds every <…>; a '<' that is not one was escaped.
var references = regexp.MustCompile(`<([^<>\n]+)>`)

// unescape undoes what Slack escapes in text, and only that.
var unescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// placeholder stands for the i-th reference while the markers are read: its number
// between private-use characters, which read as boundaries between words, as the
// reference's words would.
func placeholder(i int) string { return "\U000F0000" + strconv.Itoa(i) + "\U000F0001" }

// render is a message's text as kith keeps it: the words, their formatting, and who
// it mentions.
func render(text string, n names) rendered {
	var out rendered
	var refs []reference
	var b strings.Builder
	last := 0
	for _, m := range references.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(unescape.Replace(text[last:m[0]]))
		ref := n.reference(unescape.Replace(text[m[2]:m[3]]))
		b.WriteString(placeholder(len(refs)))
		refs = append(refs, ref)
		last = m[1]
	}
	b.WriteString(unescape.Replace(text[last:]))
	withRefs := b.String()

	links := false
	words := make([]string, 0, 2*len(refs))
	markups := make([]string, 0, 2*len(refs))
	for i, ref := range refs {
		markup := html.EscapeString(ref.words)
		if ref.link != "" {
			links = true
			markup = `<a href="` + html.EscapeString(ref.link) + `">` + markup + "</a>"
		}
		words = append(words, placeholder(i), ref.words)
		markups = append(markups, placeholder(i), markup)
		if ref.mention != nil {
			out.mentions = append(out.mentions, *ref.mention)
		}
		out.mentioned = out.mentioned || ref.mentionsMe
	}
	out.body = strings.NewReplacer(words...).Replace(withRefs)
	markup, formatted := richtext.FromMarkers(withRefs)
	if !formatted && !links {
		return out
	}
	f := richtext.FromMarkup(richtext.Sanitize(strings.NewReplacer(markups...).Replace(markup)))
	if f.IsZero() || f.Text() == "" {
		return out // formatting around nothing: the text as written
	}
	// The words are what the formatting draws over, exactly: the two never disagree.
	out.body, out.format = f.Text(), f
	return out
}

// reference reads what is inside one <…>.
func (n names) reference(inner string) reference {
	target, label, labeled := strings.Cut(inner, "|")
	switch {
	case strings.HasPrefix(target, "@"):
		id := target[1:]
		name := n.user(id)
		if name == "" {
			name = cmpOr(label, id)
		}
		words := "@" + name
		return reference{
			words:      words,
			mention:    &domain.Mention{UserID: personID(n.team, id), Name: words},
			mentionsMe: id == n.me,
		}
	case strings.HasPrefix(target, "#"):
		id := target[1:]
		name := label
		if name == "" {
			name = cmpOr(n.channel(id), id)
		}
		words := "#" + name
		return reference{words: words, mention: &domain.Mention{RoomID: string(roomID(n.team, id)), Name: words}}
	case target == "!here" || target == "!channel" || target == "!everyone":
		return reference{words: "@" + target[1:], mentionsMe: true}
	case strings.HasPrefix(target, "!"):
		// A user group (<!subteam^S…|@team>), a date (<!date^…|fallback>): Slack
		// gives the words to show.
		if labeled {
			return reference{words: label}
		}
		return reference{words: "@" + strings.TrimPrefix(target, "!")}
	}
	words := strings.TrimPrefix(target, "mailto:")
	if labeled && label != "" {
		words = label
	}
	return reference{words: words, link: target}
}

// cmpOr is the first non-empty string.
func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// escape is text as Slack takes it: &, < and > are its own.
var escape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// composed is a draft as Slack takes it: escaped, each mention of a person in this
// workspace written as Slack writes it (<@U…>), and Markdown in mrkdwn's markers
// unless the draft is to go as typed.
func composed(draft domain.Draft, team string) string {
	text := escape.Replace(draft.Body)
	for _, m := range draft.LiveMentions() {
		if user, ok := userOf(m.UserID, team); ok {
			text = strings.Replace(text, escape.Replace(m.Name), "<@"+user+">", 1)
		}
	}
	if draft.Plain {
		return text
	}
	return richtext.ToMarkers(text, func(words, address string) string {
		if words == address {
			return "<" + address + ">"
		}
		return "<" + address + "|" + words + ">"
	})
}

// userOf is the user ID behind a person ID of this workspace's.
func userOf(person, team string) (string, bool) {
	id := domain.ParseID(person)
	if id.Network != domain.ProtocolSlack || id.Account != "" {
		return "", false
	}
	user, ok := strings.CutPrefix(id.Native, team+".")
	return user, ok && user != ""
}
