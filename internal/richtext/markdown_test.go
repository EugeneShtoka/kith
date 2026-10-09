package richtext

import "testing"

// Formatting comes back as the Markdown a person would have typed for it, and words
// that only look like Markdown stay words.
func TestFormattingIsWrittenAsMarkdown(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		text  string
		spans []Span
		want  string
	}{
		"plain, with what Markdown would read": {"2*3 = 6, a_b, [x] | # not a heading", nil, `2\*3 = 6, a\_b, \[x\] \| # not a heading`},
		"a line starting with #":               {"# not a heading\n> nor a quote", nil, "\\# not a heading\n\\> nor a quote"},
		"bold inside italic":                   {"very bold idea", []Span{{Start: 0, End: 14, Italic: true}, {Start: 5, End: 9, Bold: true}}, "_very _**_bold_**_ idea_"},
		"a link":                               {"see the docs", []Span{{Start: 4, End: 12, Link: true, Href: "https://x.org/a_(b)"}}, "see [the docs](https://x.org/a_(b%29)"},
		"inline code keeps its words":          {"run make *all*", []Span{{Start: 4, End: 14, Code: true}}, "run `make *all*`"},
		"a code block":                         {"x\ny", []Span{{Start: 0, End: 3, Code: true}}, "```\nx\ny\n```"},
		"a quote over two lines":               {"said\nhi there\nok", []Span{{Start: 0, End: 13, Quote: true}}, "> said\n> hi there\nok"},
		"a spoiler and a strike":               {"it was him", []Span{{Start: 7, End: 10, Spoiler: true}, {Start: 0, End: 2, Strike: true}}, "~~it~~ was ||him||"},
		"underline is its words":               {"under", []Span{{Start: 0, End: 5, Underline: true}}, "under"},
	} {
		if got := Markdown(tc.text, tc.spans); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}
