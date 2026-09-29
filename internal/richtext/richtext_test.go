package richtext

import (
	"strings"
	"testing"
)

// The two halves of one table: what survives the door, and what it draws as.

func TestSanitizeKeepsWhatCanBeDrawn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "<strong>yes</strong>", "<strong>yes</strong>"},
		{"italic", "<em>maybe</em>", "<em>maybe</em>"},
		{"inline code", "<code>make check</code>", "<code>make check</code>"},
		{"strikethrough", "<del>no</del>", "<del>no</del>"},
		{"a named link", `<a href="https://example.org/pr/893">Query optimization</a>`,
			`<a href="https://example.org/pr/893">Query optimization</a>`},
		{"a quote", "<blockquote>they said</blockquote>", "<blockquote>they said</blockquote>"},
		{"a line break", "one<br>two", "one<br>two"},
		{"nesting", "<strong>bold <em>and italic</em></strong>",
			"<strong>bold <em>and italic</em></strong>"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Sanitize(c.in); got != c.want {
				t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Everything this client cannot draw goes, and none of it reaches the cache.
func TestSanitizeDropsWhatCannotBeDrawn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		gone []string
	}{
		{"a script, contents and all", "<b>hi</b><script>alert(1)</script>",
			[]string{"script", "alert", "(1)"}},
		{"a style block", "<b>hi</b><style>body{display:none}</style>",
			[]string{"style", "display"}},
		{"an event handler", `<b onclick="steal()">text</b>`, []string{"onclick", "steal"}},
		{"a javascript link", `<a href="javascript:alert(1)">click</a>`, []string{"javascript"}},
		{"a data link", `<a href="data:text/html;base64,xx">click</a>`, []string{"data:"}},
		{"an image", `<b>hi</b><img src="mxc://x/y" alt="a picture">`, []string{"img", "mxc"}},
		{"a table", "<b>hi</b><table><tr><td>cell</td></tr></table>",
			[]string{"table", "<tr>", "<td>"}},
		{"chosen colors", `<span data-mx-color="#ff0000">red</span>`,
			[]string{"data-mx-color", "span"}},
		{"a style attribute", `<b style="display:none">still here</b>`, []string{"style"}},
		{"an iframe", `<b>hi</b><iframe src="https://evil.example"></iframe>`, []string{"iframe"}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := Sanitize(c.in)
			for _, banned := range c.gone {
				if strings.Contains(got, banned) {
					t.Errorf("Sanitize(%q) = %q, which still carries %q", c.in, got, banned)
				}
			}
		})
	}
}

// The words a dropped tag was wrapped around stay, though — removing `<table>` must not
// remove what was in the cells.
func TestSanitizeKeepsTheWordsInsideDroppedMarkup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    string
		words string
	}{
		{"<b>keep</b><table><tr><td>cell</td></tr></table>", "cell"},
		{`<b>keep</b><span data-mx-color="#f00">red</span>`, "red"},
		{`<b>still here</b> and <div>after</div>`, "after"},
	}
	for _, c := range tests {
		if got := Parsed(Sanitize(c.in)); !strings.Contains(got, c.words) {
			t.Errorf("Sanitize(%q) draws %q, which lost %q", c.in, got, c.words)
		}
	}
}

// A formatted body that says exactly what the plain body says is not worth carrying: the
// cache would store the message twice and the renderer would be asked a question with no
// answer in it.
func TestSanitizeDropsAFormattedBodyWithNoFormatting(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "   ", "just words", "<p>just words</p>", "<div>words</div>"} {
		if got := Sanitize(in); got != "" {
			t.Errorf("Sanitize(%q) = %q, want it dropped", in, got)
		}
	}
}

// Unbalanced markup is what a buggy client and a hostile one both produce.
func TestSanitizeClosesWhatWasLeftOpen(t *testing.T) {
	t.Parallel()

	got := Sanitize("<strong>never closed")
	if !strings.HasSuffix(got, "</strong>") {
		t.Errorf("Sanitize left a tag open: %q", got)
	}
	if got := Sanitize("plain</strong>more"); strings.Contains(got, "</strong>") {
		t.Errorf("a stray closing tag survived: %q", got)
	}
}

// Parse is the drawing half: text a terminal can wrap and reorder, plus the byte ranges
// to emphasize in it.
func TestParseFlattensToTextAndSpans(t *testing.T) {
	t.Parallel()

	text, spans := Parse("this is <strong>important</strong> today")
	if want := "this is important today"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want one", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != "important" {
		t.Errorf("the span covers %q, want \"important\"", got)
	}
	if !spans[0].Bold {
		t.Errorf("the span is not bold: %+v", spans[0])
	}
}

// Nesting is a set, not a winner: bold inside a link is both, and each span says what
// applies to its own range.
func TestParseKeepsNestedEmphasis(t *testing.T) {
	t.Parallel()

	text, spans := Parse(`<a href="https://example.org">see <strong>this</strong></a>`)
	if want := "see this"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	var link, bold bool
	for _, s := range spans {
		if s.Link && text[s.Start:s.End] == "see this" && s.Href == "https://example.org" {
			link = true
		}
		if s.Bold && text[s.Start:s.End] == "this" {
			bold = true
		}
	}
	if !link || !bold {
		t.Errorf("link=%v bold=%v in %+v", link, bold, spans)
	}
}

// The link's target is kept apart from its text, which is the whole point: a named link
// is a name the sender chose and an address the plain body never carried.
func TestParseKeepsTheLinkTarget(t *testing.T) {
	t.Parallel()

	text, spans := Parse(`<a href="https://example.org/pull/893">Query optimization</a>`)
	if text != "Query optimization" {
		t.Errorf("text = %q", text)
	}
	if len(spans) != 1 || spans[0].Href != "https://example.org/pull/893" {
		t.Errorf("spans = %+v, want the href kept", spans)
	}
}

// Block tags end the line, because that is the whole of what a terminal can say about
// block structure without a layout engine.
func TestParseTurnsBlocksIntoLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"paragraphs", "<p>one</p><p>two</p>", "one\ntwo"},
		{"a break", "one<br>two", "one\ntwo"},
		{"a list", "<ul><li>one</li><li>two</li></ul>", "• one\n• two"},
		{"a quote", "<blockquote>said</blockquote>after", "said\nafter"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if text, _ := Parse(c.in); text != c.want {
				t.Errorf("Parse(%q) text = %q, want %q", c.in, text, c.want)
			}
		})
	}
}

// The whole way through, on exactly what the renderer produced for a message typed as
// "**bold** and __italic__\n- item 1\n- item 2".
func TestARealListSurvivesTheDoorAndDrawsItsBullets(t *testing.T) {
	t.Parallel()

	const rendered = "<p><strong>bold</strong> and <strong>italic</strong></p>\n" +
		"<ul>\n<li>item 1</li>\n<li>item 2</li>\n</ul>"

	kept := Sanitize(rendered)
	for _, tag := range []string{"<ul>", "<li>"} {
		if !strings.Contains(kept, tag) {
			t.Fatalf("the sanitizer dropped %s:\n  %q", tag, kept)
		}
	}
	text, spans := Parse(kept)
	if want := "bold and italic\n• item 1\n• item 2"; text != want {
		t.Errorf("drew\n%q\nwant\n%q", text, want)
	}
	if len(spans) != 2 {
		t.Errorf("spans = %+v, want the two bold runs", spans)
	}
}

// Parsed is the text half of Parse, for the tests that only care what words survived.
func Parsed(formatted string) string {
	text, _ := Parse(formatted)
	return text
}

// A spoiler is the one span that survives, and the reason it must is that dropping it
// *revealed* what somebody covered up — the worst outcome available, since the sender
// took an action and the client quietly undid it.
func TestASpoilerSurvivesAndAColoredSpanDoesNot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		spoiler bool
		reason  string
		text    string
	}{
		{
			name: "a plain spoiler", in: `he dies at the <span data-mx-spoiler>end</span>`,
			spoiler: true, text: "he dies at the end",
		},
		{
			name: "a spoiler with a label", in: `<span data-mx-spoiler="plot">he dies</span>`,
			spoiler: true, reason: "plot", text: "he dies",
		},
		{
			// Kept alongside a mark that does survive, so the case is "the span went"
			// rather than "the whole thing went": a formatted body with nothing left
			// to draw is dropped entirely, which is tested above.
			name: "a span that is only a color", in: `<b>keep</b><span data-mx-color="#f00">red</span>`,
			spoiler: false, text: "keepred",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			text, spans := Parse(Sanitize(c.in))
			if text != c.text {
				t.Errorf("text = %q, want %q", text, c.text)
			}
			var got *Span
			for i := range spans {
				if spans[i].Spoiler {
					got = &spans[i]
				}
			}
			switch {
			case c.spoiler && got == nil:
				t.Fatalf("the spoiler was lost: %q → %+v", c.in, spans)
			case !c.spoiler && got != nil:
				t.Fatalf("a span that is not a spoiler became one: %+v", *got)
			case c.spoiler && got.Reason != c.reason:
				t.Errorf("reason = %q, want %q", got.Reason, c.reason)
			}
		})
	}
}
