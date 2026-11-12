package slack

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	out.body = emojified(strings.NewReplacer(words...).Replace(withRefs))
	markup, formatted := richtext.FromMarkers(withRefs)
	if !formatted && !links {
		return out
	}
	f := richtext.FromMarkup(richtext.Sanitize(strings.NewReplacer(markups...).Replace(markup)))
	if f.IsZero() || f.Text() == "" {
		return out // formatting around nothing: the text as written
	}
	// The formatting is drawn over exactly these words, so the two never disagree.
	out.body, out.format = Emojify(f.Text(), f)
	return out
}

// Slack writes an emoji as its name between colons (":tada:", ":wave::skin-tone-3:")
// and shows the emoji; kith keeps the emoji, so every client shows it. A name may
// touch a word on one side ("aligned:pray:"), not on both (10:30:00, x.io/a:b:c
// stay); never in code or a link; and a name Slack does not know stays as typed.

// Emojify is a message's words and formatting with its emoji names turned into the
// emoji. Formatting is converted as markup and its words read back from it, so its
// spans fall on the converted words.
func Emojify(body string, format richtext.Formatted) (string, richtext.Formatted) {
	if format.Markup() == "" {
		return emojified(body), format
	}
	f := richtext.FromMarkup(emojifiedMarkup(format.Markup()))
	return f.Text(), f
}

// shortcode is an emoji name at the start of the text, a skin tone after it as Slack
// writes one.
var shortcode = regexp.MustCompile(`^:([a-z0-9_+-]+):(?::(skin-tone-[2-6]):)?`)

// emojified is text with its emoji names turned into the emoji.
func emojified(text string) string {
	var b strings.Builder
	last := 0
	for i := strings.IndexByte(text, ':'); i >= 0 && i < len(text); i++ {
		if text[i] != ':' {
			continue
		}
		m := shortcode.FindStringSubmatchIndex(text[i:])
		if m == nil {
			continue
		}
		end := i + m[1]
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if wordy(before) && wordy(after) {
			continue // inside a word on both sides: a path, a time
		}
		name := text[i+m[2] : i+m[3]]
		if m[4] >= 0 {
			name += "::" + text[i+m[4]:i+m[5]]
		}
		if e, ok := named(name); ok {
			b.WriteString(text[last:i] + e)
			last = end
		}
		i = end - 1
	}
	if last == 0 {
		return text
	}
	return b.String() + text[last:]
}

// wordy reports whether r continues a word or a path.
func wordy(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '/'
}

// tags finds markup's tags; sanitized markup escapes every other '<'.
var tags = regexp.MustCompile(`<(/?)([a-z0-9]+)[^>]*>`)

// verbatim are the tags whose words are left as written: code and links.
var verbatim = map[string]bool{"code": true, "pre": true, "a": true}

// emojifiedMarkup is sanitized markup with the emoji names in its words, outside code
// and links, turned into the emoji. A tag counts as a word's edge.
func emojifiedMarkup(markup string) string {
	var b strings.Builder
	last, inside := 0, 0
	for _, m := range tags.FindAllStringSubmatchIndex(markup, -1) {
		words := markup[last:m[0]]
		if inside == 0 {
			words = emojified(words)
		}
		b.WriteString(words + markup[m[0]:m[1]])
		if verbatim[markup[m[4]:m[5]]] {
			if m[3] > m[2] {
				inside--
			} else {
				inside++
			}
		}
		last = m[1]
	}
	if inside == 0 {
		return b.String() + emojified(markup[last:])
	}
	return b.String() + markup[last:]
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
