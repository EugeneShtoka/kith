package markdown

import (
	"strings"
	"testing"
)

func TestTheRendererKeepsKithsConventions(t *testing.T) {
	for _, c := range []struct{ source, has, hasNot string }{
		{"*x*", "<em>x</em>", ""},
		{"__x__", "<strong>x</strong>", ""},
		{"~~x~~", "<del>x</del>", ""},
		{"cd ~/src, ~5", "~/src", "<del>"},
		{"||secret||", "data-mx-spoiler", ""},
		{"`a || b`", "<code>a || b</code>", "spoiler"},
		{"<script>x</script>", "&lt;script&gt;", "<script>"},
		{"| a |\n|---|\n| b |", "", "<table>"},
	} {
		got, err := HTML(c.source)
		if err != nil {
			t.Fatalf("HTML(%q): %v", c.source, err)
		}
		if c.has != "" && !strings.Contains(got, c.has) {
			t.Errorf("HTML(%q) = %q, want it to have %q", c.source, got, c.has)
		}
		if c.hasNot != "" && strings.Contains(got, c.hasNot) {
			t.Errorf("HTML(%q) = %q, want no %q", c.source, got, c.hasNot)
		}
	}
}
