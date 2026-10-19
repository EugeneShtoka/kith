package slack

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Slack text is mrkdwn: WhatsApp's markers (*bold*, _italic_, ~strike~, `code`,
// ```blocks```) with &, < and > escaped, and references in angle brackets: <@U…> a
// person, <#C…|name> a channel, <!here>, <https://…|words> a link.

// names resolves a message's references to words.
type names struct {
	team string
	// me is the person's user ID: a mention of them, or of everyone, is theirs.
	me string
	// user and channel name a user or conversation by ID, "" when unknown.
	user    func(id string) string
	channel func(id string) string
}

type rendered struct {
	body      string
	format    richtext.Formatted
	mentions  []domain.Mention
	mentioned bool
}

// reference is one <…>: its words, its link markup ("" for words only), and whom it
// mentions.
type reference struct {
	words, link string
	mention     *domain.Mention
	mentionsMe  bool
}

// references finds every <…>; a bare '<' would have been escaped.
var references = regexp.MustCompile(`<([^<>\n]+)>`)

// unescape undoes Slack's escaping, and only that.
var unescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// placeholder stands for the i-th reference while markers are parsed: its number
// between private-use characters, which act as word boundaries like the words would.
func placeholder(i int) string { return "\U000F0000" + strconv.Itoa(i) + "\U000F0001" }

// render is a message's text as kith keeps it: words, formatting and mentions.
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
	// The formatting is drawn over exactly these words, so the two never disagree.
	out.body, out.format = f.Text(), f
	return out
}

// reference parses the inside of one <…>.
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
		// A user group (<!subteam^S…|@team>) or date (<!date^…|fallback>): Slack gives the
		// words to show.
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

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// escape escapes &, < and > for Slack.
var escape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// typedBroadcasts finds a typed @here, @channel or @everyone, standing as a word of its own.
var typedBroadcasts = regexp.MustCompile(`(^|[^\pL\pN_.@/])@(here|channel|everyone)\b`)

// composed is a draft as Slack takes it: escaped, mentions of this workspace's people
// as <@U…> and @here, @channel or @everyone as Slack's, and Markdown converted to
// mrkdwn unless sent as typed.
func composed(draft domain.Draft, team string) string {
	text := escape.Replace(draft.Body)
	for _, m := range draft.LiveMentions() {
		if user, ok := userOf(m.UserID, team); ok {
			text = strings.Replace(text, escape.Replace(m.Name), "<@"+user+">", 1)
		}
	}
	text = typedBroadcasts.ReplaceAllString(text, "$1<!$2>")
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

// userOf is the user ID behind a person ID of this workspace.
func userOf(person, team string) (string, bool) {
	id := domain.ParseID(person)
	if id.Network != domain.ProtocolSlack || id.Account != "" {
		return "", false
	}
	user, ok := strings.CutPrefix(id.Native, team+".")
	return user, ok && user != ""
}
