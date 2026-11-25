// SPDX-License-Identifier: Unlicense OR MIT

package seltext

import (
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
)

// laidOut draws spans width px wide with Go's fonts only, so the boxes are the same on
// every machine, and returns the state the frame left.
func laidOut(t *testing.T, width int, spans ...SpanStyle) (*State, layout.Dimensions) {
	t.Helper()
	shaper := text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Collection()))
	gtx := layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(width, 1000)},
	}
	st := new(State)
	dims := Text(st, shaper, spans...).Layout(gtx)
	return st, dims
}

func span(s string) SpanStyle { return SpanStyle{Size: 16, Content: s} }

// The clusters cover every byte once, in order, across spans and wrapped lines, so any
// selection maps back to the text exactly.
func TestClustersCoverTheTextInOrder(t *testing.T) {
	for name, spans := range map[string][]SpanStyle{
		"one line":   {span("Hello "), {Size: 16, Content: "world", Font: font.Font{Weight: font.Bold}}},
		"wrapped":    {span("the quick brown fox jumps over the lazy dog again and again")},
		"newlines":   {span("first\nsecond\n"), span("third")},
		"multi-byte": {span("Лев Толстой — ü"), span(" ok")},
	} {
		t.Run(name, func(t *testing.T) {
			st, _ := laidOut(t, 120, spans...)
			at := 0
			for _, c := range st.clusters {
				if c.start != at || c.end <= c.start {
					t.Fatalf("cluster %d..%d after byte %d: the clusters leave a gap or overlap", c.start, c.end, at)
				}
				at = c.end
			}
			if at != len(st.text) {
				t.Fatalf("clusters end at byte %d of %d", at, len(st.text))
			}
		})
	}
}

func TestOffsetAtFollowsThePointer(t *testing.T) {
	st, dims := laidOut(t, 120, span("the quick brown fox jumps over the lazy dog"))
	first := st.clusters[0].box
	if got := st.offsetAt(f32.Pt(0, float32(first.Min.Y+1))); got != 0 {
		t.Errorf("the left edge of the first line is byte %d, want 0", got)
	}
	if got := st.offsetAt(f32.Pt(float32(dims.Size.X+50), float32(dims.Size.Y+50))); got != len(st.text) {
		t.Errorf("past the end of the text is byte %d, want %d", got, len(st.text))
	}
	lines := map[int]bool{}
	for _, c := range st.clusters {
		lines[c.box.Min.Y] = true
	}
	if len(lines) < 2 {
		t.Fatalf("the text did not wrap at 120px (%d line)", len(lines))
	}
	// The middle of each cluster's right half is that cluster's end.
	for _, c := range st.clusters {
		mid := c.box.Min.X + c.box.Dx()*3/4
		if c.box.Dx() < 4 {
			continue
		}
		if got := st.offsetAt(f32.Pt(float32(mid), float32(c.box.Min.Y+1))); got != c.end {
			t.Fatalf("right half of %q is byte %d, want %d", st.text[c.start:c.end], got, c.end)
		}
	}
}

func TestSelectedTextIsTheBytesBetween(t *testing.T) {
	st, _ := laidOut(t, 400, span("copy "), SpanStyle{Size: 16, Content: "this", Font: font.Font{Weight: font.Bold}}, span(" part"))
	st.anchor, st.caret = 10, 5 // dragged right to left
	if got := st.SelectedText(); got != "this " {
		t.Errorf("selected %q, want %q", got, "this ")
	}
}

func TestLinkAtIsWhereTheLinkIsDrawn(t *testing.T) {
	st, _ := laidOut(t, 400, span("see "), SpanStyle{Size: 16, Content: "kith", Link: "https://example.org"}, span(" now"))
	var in, out image.Point
	for _, c := range st.clusters {
		mid := image.Pt((c.box.Min.X+c.box.Max.X)/2, (c.box.Min.Y+c.box.Max.Y)/2)
		switch st.text[c.start:c.end] {
		case "i":
			in = mid
		case "w":
			out = mid
		}
	}
	if got := st.linkAt(f32.Pt(float32(in.X), float32(in.Y))); got != "https://example.org" {
		t.Errorf("link under the link's letters is %q", got)
	}
	if got := st.linkAt(f32.Pt(float32(out.X), float32(out.Y))); got != "" {
		t.Errorf("link under plain text is %q, want none", got)
	}
}
