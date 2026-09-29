package matrix

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestMarkdownBecomesHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{"bold", "this is **important**", "this is <strong>important</strong>"},
		{"italic", "this is _subtle_", "this is <em>subtle</em>"},
		{"inline code", "run `make check` first", "run <code>make check</code> first"},
		{"strikethrough", "~~never mind~~", "<del>never mind</del>"},
		{"a named link", "see [the PR](https://example.org/pr/893)",
			`see <a href="https://example.org/pr/893">the PR</a>`},
		{"a quote", "> they said this", "<blockquote>"},
		{"a list", "- one\n- two", "<li>one</li>"},
		{"a fenced block", "```\nx := 1\n```", "<pre><code>x := 1"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := renderBody(c.body, nil)
			if !strings.Contains(got, c.want) {
				t.Errorf("rendered %q as\n  %q\nwanting %q in it", c.body, got, c.want)
			}
		})
	}
}

// An ordinary sentence sends no HTML at all.
func TestPlainProseSendsNoHTML(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"are we still on for tomorrow?",
		"שלום, מה קורה",
		"3 * 4 = 12",
		"a_b_c is a name",
	} {
		if got := renderBody(body, nil); got != "" {
			t.Errorf("%q produced a formatted body: %q", body, got)
		}
	}
}

// Typed HTML is text and arrives escaped.
func TestTypedHTMLIsEscapedRatherThanSent(t *testing.T) {
	t.Parallel()

	got := renderBody("careful: <b>not bold</b> and <script>alert(1)</script>", nil)
	if strings.Contains(got, "<b>") || strings.Contains(got, "<script>") {
		t.Errorf("typed HTML reached the formatted body:\n  %q", got)
	}
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Errorf("the tag was not escaped either:\n  %q", got)
	}
}

func TestAMentionPillsThroughTheRenderer(t *testing.T) {
	t.Parallel()

	mentions := []domain.Mention{{UserID: "@alice:x", Name: "Alice"}}
	got := renderBody("**hello** Alice", mentions)
	if want := `<a href="https://matrix.to/#/@alice:x">Alice</a>`; !strings.Contains(got, want) {
		t.Errorf("no pill in\n  %q\nwanted %q", got, want)
	}
	if !strings.Contains(got, "<strong>hello</strong>") {
		t.Errorf("the formatting was lost while pilling:\n  %q", got)
	}
}

// Longest name first, first occurrence only.
func TestPillingKeepsItsOrderAndFiresOnce(t *testing.T) {
	t.Parallel()

	got := renderBody("Dan and Daniel, then Dan again", []domain.Mention{
		{UserID: "@dan:x", Name: "Dan"},
		{UserID: "@daniel:x", Name: "Daniel"},
	})
	if !strings.Contains(got, `<a href="https://matrix.to/#/@daniel:x">Daniel</a>`) {
		t.Errorf("Daniel was eaten by Dan:\n  %q", got)
	}
	if n := strings.Count(got, "matrix.to/#/@dan:x"); n != 1 {
		t.Errorf("Dan pilled %d times, want once:\n  %q", n, got)
	}
	if !strings.HasSuffix(got, "then Dan again") {
		t.Errorf("the second Dan was pilled too:\n  %q", got)
	}
}

// Markdown characters in a name are escaped in the link text.
func TestAMentionWithMarkdownInItsNameIsEscaped(t *testing.T) {
	t.Parallel()

	got := renderBody("hi a_b_c", []domain.Mention{{UserID: "@ab:x", Name: "a_b_c"}})
	if !strings.Contains(got, ">a_b_c</a>") {
		t.Errorf("the name was rendered as markup:\n  %q", got)
	}
}

// Plain mode sends the characters as typed and still pills mentions.
func TestPlainSendsTheCharactersAndStillPills(t *testing.T) {
	t.Parallel()

	draft := domain.Draft{Body: "**not bold** Alice", Plain: true,
		Mentions: []domain.Mention{{UserID: "@alice:x", Name: "Alice"}}}
	got := buildMessage(draft, "")
	if got.Body != draft.Body {
		t.Errorf("the plain body is %q, want what was typed", got.Body)
	}
	if strings.Contains(got.FormattedBody, "<strong>") {
		t.Errorf("a plain message was rendered:\n  %q", got.FormattedBody)
	}
	if !strings.Contains(got.FormattedBody, "matrix.to/#/@alice:x") {
		t.Errorf("a plain message dropped its pill:\n  %q", got.FormattedBody)
	}
}

func TestPlainWithNoMentionsIsAnOrdinaryEvent(t *testing.T) {
	t.Parallel()

	got := buildMessage(domain.Draft{Body: "**stars**", Plain: true}, "")
	if got.FormattedBody != "" || got.Format != "" {
		t.Errorf("format=%q formatted_body=%q, want neither", got.Format, got.FormattedBody)
	}
}

// The plain body is always what was typed.
func TestTheBodyIsAlwaysWhatWasTyped(t *testing.T) {
	t.Parallel()

	typed := "**bold** and `code` and a [link](https://example.org)"
	got := buildMessage(domain.Draft{Body: typed}, "")
	if got.Body != typed {
		t.Errorf("body = %q, want %q", got.Body, typed)
	}
	if got.Format != event.FormatHTML {
		t.Errorf("format = %q, want it rendered", got.Format)
	}
}

// `||` is a spoiler in prose but not inside code spans or fenced blocks.
func TestSpoilersAreWrittenButNotOutOfSomebodysCode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body     string
		spoilers bool
	}{
		"a spoiler in prose": {
			body:     "the killer is ||the butler||",
			spoilers: true,
		},
		"a boolean in a code span": {
			body:     "still has the broken `indexOf(false) || 0` in it",
			spoilers: false,
		},
		"a boolean in a fenced block": {
			body:     "```\nconst slug = a?.slug?.trim() ||\n  (b ? norm(b.name) : '');\n```",
			spoilers: false,
		},
		"a single pipe is nothing at all": {
			body:     "run it | grep failed",
			spoilers: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			html := renderBody(tc.body, nil)
			if got := strings.Contains(html, "data-mx-spoiler"); got != tc.spoilers {
				t.Errorf("spoiler markup = %v, want %v\nhtml: %s", got, tc.spoilers, html)
			}
		})
	}
}

// A room mention becomes a link in both render paths.
func TestARoomMentionBecomesALink(t *testing.T) {
	t.Parallel()

	mentions := []domain.Mention{{RoomID: "!war:example.org", Name: "Warroom"}}
	html := renderBody("see Warroom for that", mentions)
	if !strings.Contains(html, `href="https://matrix.to/#/!war:example.org"`) {
		t.Errorf("markdown render = %q, want a link to the room", html)
	}
	if !strings.Contains(html, ">Warroom<") {
		t.Errorf("markdown render = %q, want the room's name as the link text", html)
	}

	plain := pillHTML("see Warroom for that", mentions)
	if !strings.Contains(plain, `href="https://matrix.to/#/!war:example.org"`) {
		t.Errorf("plain render = %q, want the same link", plain)
	}
}

// People and rooms pill together, longest name first across both.
func TestAMessageCanNameAPersonAndARoom(t *testing.T) {
	t.Parallel()

	mentions := []domain.Mention{
		{UserID: "@ada:example.org", Name: "Ada"},
		{RoomID: "!war:example.org", Name: "Warroom"},
	}
	html := renderBody("Ada is in Warroom", mentions)
	if !strings.Contains(html, "@ada:example.org") || !strings.Contains(html, "!war:example.org") {
		t.Errorf("render = %q, want both links", html)
	}
}
