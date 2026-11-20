// SPDX-License-Identifier: Unlicense OR MIT

package seltext

import (
	"image"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"
)

// textIterator is styledtext's: it measures and paints one line of a span. It also
// records each grapheme cluster's bytes in content and its horizontal extent.
type textIterator struct {
	viewport   image.Rectangle
	maxLines   int
	linesSeen  int
	init       bool
	firstX     fixed.Int26_6
	hasNewline bool
	lineOff    image.Point
	padding    image.Rectangle
	bounds     image.Rectangle
	runes      int
	visible    bool
	first      bool
	baseline   int

	content  string
	byteAt   int // bytes of content the recorded clusters cover
	inside   bool
	minX     int // the open cluster's extent so far
	maxX     int
	clusters []cluster
}

func (it *textIterator) processGlyph(g text.Glyph, ok bool) (_ text.Glyph, visibleOrBefore bool) {
	logicalBounds := image.Rectangle{
		Min: image.Pt(g.X.Floor(), int(g.Y)-g.Ascent.Ceil()),
		Max: image.Pt((g.X + g.Advance).Ceil(), int(g.Y)+g.Descent.Ceil()),
	}
	if g.Flags&text.FlagTruncator != 0 {
		if it.runes == 0 {
			it.hasNewline = true
		}
		it.bounds.Min.Y = min(it.bounds.Min.Y, logicalBounds.Min.Y)
		it.bounds.Max.Y = max(it.bounds.Max.Y, logicalBounds.Max.Y)
		return g, false
	}
	it.runes += int(g.Runes)
	it.hasNewline = it.hasNewline || (g.Flags&text.FlagLineBreak > 0 && g.Flags&text.FlagParagraphBreak > 0)
	if it.maxLines > 0 {
		if g.Flags&text.FlagLineBreak != 0 {
			it.linesSeen++
		}
		if it.linesSeen == it.maxLines && g.Flags&text.FlagParagraphBreak != 0 {
			return g, false
		}
	}
	if d := g.Bounds.Min.X.Floor(); d < it.padding.Min.X {
		it.padding.Min.X = d
	}
	if d := (g.Bounds.Max.X - g.Advance).Ceil(); d > it.padding.Max.X {
		it.padding.Max.X = d
	}
	if !it.first {
		it.first = true
		it.baseline = int(g.Y)
		it.bounds = logicalBounds
	}
	above := logicalBounds.Max.Y < it.viewport.Min.Y
	below := logicalBounds.Min.Y > it.viewport.Max.Y
	left := logicalBounds.Max.X < it.viewport.Min.X
	right := logicalBounds.Min.X > it.viewport.Max.X
	it.visible = !above && !below && !left && !right
	if it.visible {
		it.bounds.Min.X = min(it.bounds.Min.X, logicalBounds.Min.X)
		it.bounds.Min.Y = min(it.bounds.Min.Y, logicalBounds.Min.Y)
		it.bounds.Max.X = max(it.bounds.Max.X, logicalBounds.Max.X)
		it.bounds.Max.Y = max(it.bounds.Max.Y, logicalBounds.Max.Y)
	}
	it.record(g)
	return g, ok && !below
}

// record grows the open cluster by g; g's Runes, set on a cluster's last glyph, close
// it over that many runes of content.
func (it *textIterator) record(g text.Glyph) {
	x0, x1 := (g.X - it.firstX).Floor(), (g.X + g.Advance - it.firstX).Ceil()
	if !it.inside {
		it.inside, it.minX, it.maxX = true, x0, x1
	} else {
		it.minX, it.maxX = min(it.minX, x0), max(it.maxX, x1)
	}
	if g.Runes == 0 {
		return
	}
	start := it.byteAt
	for range g.Runes {
		if it.byteAt >= len(it.content) {
			break
		}
		_, n := utf8.DecodeRuneInString(it.content[it.byteAt:])
		it.byteAt += n
	}
	it.clusters = append(it.clusters, cluster{start: start, end: it.byteAt, box: image.Rect(it.minX, 0, it.maxX, 1)})
	it.inside = false
}

// paintGlyph is styledtext's, painting bitmap glyphs (color emoji) too, as widget.Label
// does.
func (it *textIterator) paintGlyph(gtx layout.Context, shaper *text.Shaper, glyph text.Glyph, line []text.Glyph) ([]text.Glyph, bool) {
	if !it.init && glyph.Flags&text.FlagTruncator == 0 {
		it.firstX = glyph.X
	}
	_, visibleOrBefore := it.processGlyph(glyph, true)
	if it.visible {
		it.init = true
		if len(line) == 0 {
			it.lineOff = image.Point{X: (glyph.X - it.firstX).Floor(), Y: int(glyph.Y)}.Sub(it.viewport.Min)
		}
		line = append(line, glyph)
	}
	if glyph.Flags&text.FlagLineBreak > 0 || cap(line)-len(line) == 0 || !visibleOrBefore {
		t := op.Offset(it.lineOff).Push(gtx.Ops)
		outline := clip.Outline{Path: shaper.Shape(line)}.Op().Push(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		outline.Pop()
		if call := shaper.Bitmaps(line); call != (op.CallOp{}) {
			call.Add(gtx.Ops)
		}
		t.Pop()
		line = line[:0]
	}
	return line, visibleOrBefore
}
