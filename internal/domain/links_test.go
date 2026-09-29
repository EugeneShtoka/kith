package domain_test

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want []string
	}{
		{"none", "just some text", nil},
		{"bare", "https://example.org", []string{"https://example.org"}},
		{"http too", "http://example.org/x", []string{"http://example.org/x"}},
		{
			name: "in a sentence, trailing stop is prose not URL",
			body: "see https://example.org/docs.",
			want: []string{"https://example.org/docs"},
		},
		{
			name: "trailing comma and semicolon",
			body: "a https://one.org, b https://two.org; c",
			want: []string{"https://one.org", "https://two.org"},
		},
		{
			name: "parenthesised link loses the paren",
			body: "(see https://example.org)",
			want: []string{"https://example.org"},
		},
		{
			name: "a link with balanced parens keeps them",
			body: "https://en.wikipedia.org/wiki/Foo_(bar)",
			want: []string{"https://en.wikipedia.org/wiki/Foo_(bar)"},
		},
		{
			name: "query strings and fragments survive",
			body: "https://example.org/a?b=c&d=e#frag",
			want: []string{"https://example.org/a?b=c&d=e#frag"},
		},
		{
			name: "several, in order, deduplicated",
			body: "https://b.org then https://a.org then https://b.org again",
			want: []string{"https://b.org", "https://a.org"},
		},
		{
			name: "across lines",
			body: "first https://one.org\nsecond https://two.org",
			want: []string{"https://one.org", "https://two.org"},
		},
		{
			name: "markdown-ish brackets",
			body: `[docs](https://example.org/docs)`,
			want: []string{"https://example.org/docs"},
		},
		// The narrowness is the point: these are not offered for opening.
		{"no file scheme", "file:///etc/passwd", nil},
		{"no javascript scheme", "javascript:alert(1)", nil},
		{"no custom scheme", "myapp://do-something", nil},
		{"no mxc", "mxc://server/media", nil},
		{"no bare host", "example.org", nil},
		{
			name: "an https link inside other text is still found",
			body: "file:///etc/passwd and https://ok.org",
			want: []string{"https://ok.org"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := domain.Links(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("Links(%q) = %v, want %v", tc.body, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("Links(%q)[%d] = %q, want %q", tc.body, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Whatever a message contains, extraction must terminate and must not offer anything
// that isn't a web link. A body is text someone else wrote.
func TestLinksOnHostileBodies(t *testing.T) {
	t.Parallel()

	bodies := []string{
		"", " ", strings.Repeat("a", 100_000),
		strings.Repeat("https://", 5000),
		"https://" + strings.Repeat("a", 50_000),
		strings.Repeat("(", 5000) + "https://x.org" + strings.Repeat(")", 5000),
		"https://\x00\x01example.org",
		"https://例え.jp/パス",
		"https://user:pass@example.org/x",
		"HTTPS://EXAMPLE.ORG",
	}
	for _, body := range bodies {
		for _, link := range domain.Links(body) {
			if !domain.OpenableLink(link) {
				t.Errorf("Links(%.40q) offered %q, which is not openable", body, link)
			}
		}
	}
}

func TestOpenableLink(t *testing.T) {
	t.Parallel()

	ok := []string{"http://x.org", "https://x.org", "HTTPS://X.ORG", "https://x.org/a?b#c"}
	for _, link := range ok {
		if !domain.OpenableLink(link) {
			t.Errorf("OpenableLink(%q) = false, want true", link)
		}
	}
	// Everything else is refused: an opener is handed whatever this returns true for.
	bad := []string{
		"", "example.org", "file:///etc/passwd", "javascript:alert(1)",
		"data:text/html,<script>", "mxc://s/m", "ftp://x.org", " https://x.org",
		"httpsx://x.org", "http:/x.org",
	}
	for _, link := range bad {
		if domain.OpenableLink(link) {
			t.Errorf("OpenableLink(%q) = true, want false", link)
		}
	}
}

// A Slack link's label is not part of its URL.
func TestALinkStopsAtTheSlackSeparator(t *testing.T) {
	t.Parallel()

	const body = "> **<https://us-east-2.console.aws.amazon.com/cloudwatch/home?region=us-east-2" +
		"#alarm:name=tipmaster-prd-cpu|✅️ CloudWatch Alarm | tipmaster-prd-cpu>**"
	const want = "https://us-east-2.console.aws.amazon.com/cloudwatch/home?region=us-east-2" +
		"#alarm:name=tipmaster-prd-cpu"

	got := domain.Links(body)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Links() = %q,\n want exactly [%q]", got, want)
	}
}

// What may be handed to the terminal as a hyperlink target.
func TestSafeHyperlink(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		link string
		want bool
	}{
		{"an ordinary url", "https://example.org/a?b=c#d", true},
		{"percent-encoded, which is what a correct IRI looks like", "https://example.org/%D1%84", true},
		{"a raw emoji, which is what the bug put there", "https://example.org/✅", false},
		{"a raw non-latin path", "https://example.org/фото", false},
		{"an embedded escape", "https://example.org/\x1b]8;;evil\a", false},
		{"a newline", "https://example.org/\na", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.SafeHyperlink(tc.link); got != tc.want {
				t.Errorf("SafeHyperlink(%q) = %v, want %v", tc.link, got, tc.want)
			}
		})
	}
}
