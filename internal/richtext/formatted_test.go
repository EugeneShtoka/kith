package richtext

import (
	"reflect"
	"testing"
)

// Formatting read from markup draws what Parse draws, keeps the markup it came from,
// and cannot be changed through what it hands out.
func TestFormattedDrawsItsMarkup(t *testing.T) {
	t.Parallel()

	const markup = `see <b>this</b> and <a href="https://example.org">that</a>`
	f := FromMarkup(markup)
	text, spans := Parse(markup)
	if f.Markup() != markup || f.Text() != text || !reflect.DeepEqual(f.Spans(), spans) {
		t.Fatalf("FromMarkup = %q %q %+v, want %q %q %+v", f.Markup(), f.Text(), f.Spans(), markup, text, spans)
	}
	f.Spans()[0].Bold = false
	if !f.Spans()[0].Bold {
		t.Error("changing the spans handed out changed the formatting")
	}
	given := []Span{{Start: 0, End: 3, Italic: true}}
	d := Drawn("see", given)
	given[0].Italic = false
	if !d.Spans()[0].Italic || d.Markup() != "" {
		t.Errorf("Drawn = %+v, markup %q: it must copy the spans and have no markup", d.Spans(), d.Markup())
	}
	for name, zero := range map[string]Formatted{
		"no markup":  FromMarkup(""),
		"nothing":    Drawn("", nil),
		"zero value": {},
	} {
		if !zero.IsZero() {
			t.Errorf("%s is formatting", name)
		}
	}
	if f.IsZero() || d.IsZero() {
		t.Error("real formatting reads as none")
	}
}
