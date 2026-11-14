// SPDX-License-Identifier: Unlicense OR MIT

// Package seltext draws text in many styles that can be selected and copied, with
// clickable links, background, underline and strikethrough, and color emoji.
//
// It is gioui.org/x/styledtext's layout (v0.10.3) keeping what that drops: where each
// grapheme cluster landed, so a pointer maps to a byte of the text. styledtext and
// gioui.org/x/richtext over it cannot select, and paint only glyph outlines, so bitmap
// (color) emoji vanish there.
package seltext

import (
	"image"
	"image/color"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"golang.org/x/image/math/fixed"
)

// SpanStyle is one run of text and how it is drawn.
type SpanStyle struct {
	Font    font.Font
	Size    unit.Sp
	Color   color.NRGBA
	Content string
	// Background fills behind the run when not transparent.
	Background               color.NRGBA
	Underline, Strikethrough bool
	// Link, when set, makes the run clickable: State.Clicked reports it.
	Link string

	idx   int
	start int // byte offset of Content in the whole text
}

// cluster is one grapheme cluster as drawn: its bytes in the whole text and its box.
type cluster struct {
	start, end int
	box        image.Rectangle
}

// shape is one span's piece on one line, as styledtext's spanShape plus its clusters.
type shape struct {
	offset   image.Point
	call     op.CallOp
	size     image.Point
	ascent   int
	clusters []cluster // boxes relative to the shape
}

// TextStyle is rich text to draw with state.
type TextStyle struct {
	State     *State
	Styles    []SpanStyle
	Alignment text.Alignment
	// Selection colors the selected text's background.
	Selection color.NRGBA
	*text.Shaper
}

// Text constructs a TextStyle.
func Text(state *State, shaper *text.Shaper, styles ...SpanStyle) TextStyle {
	return TextStyle{State: state, Styles: styles, Shaper: shaper}
}

type spanResult struct {
	call             op.CallOp
	width, height    int
	ascent           int
	runes            int
	multiLine        bool
	endedWithNewline bool
	clusters         []cluster
}

func (t TextStyle) iterateSpan(gtx layout.Context, maxWidth int, span SpanStyle, truncate bool) (op.CallOp, textIterator) {
	var glyphs [32]text.Glyph
	maxLines := 0
	if truncate {
		maxLines = 1
	}
	macro := op.Record(gtx.Ops)
	paint.ColorOp{Color: span.Color}.Add(gtx.Ops)
	t.Shaper.LayoutString(text.Parameters{
		Font:       span.Font,
		PxPerEm:    fixed.I(gtx.Sp(span.Size)),
		MaxLines:   maxLines,
		MaxWidth:   maxWidth,
		Truncator:  "​",
		Locale:     gtx.Locale,
		WrapPolicy: text.WrapWords,
	}, span.Content)
	ti := textIterator{viewport: image.Rectangle{Max: gtx.Constraints.Max}, maxLines: 1, content: span.Content}
	line := glyphs[:0]
	for g, ok := t.Shaper.NextGlyph(); ok; g, ok = t.Shaper.NextGlyph() {
		line, ok = ti.paintGlyph(gtx, t.Shaper, g, line)
		if !ok {
			break
		}
	}
	return macro.Stop(), ti
}

func (t TextStyle) layoutSpan(gtx layout.Context, maxWidth int, span SpanStyle) spanResult {
	call, ti := t.iterateSpan(gtx, maxWidth, span, true)
	runesDisplayed := ti.runes
	multiLine := runesDisplayed < utf8.RuneCountInString(span.Content)
	endedWithNewline := ti.hasNewline
	if multiLine {
		var i int
		for i = 0; i < runesDisplayed; {
			_, sz := utf8.DecodeRuneInString(span.Content[i:])
			i += sz
		}
		firstTruncatedRune, _ := utf8.DecodeRuneInString(span.Content[i:])
		if firstTruncatedRune == '\n' {
			endedWithNewline = true
			runesDisplayed++
			// The newline is cut, not drawn: it is a cluster of no width at the line's
			// end, so a selection over it copies the line break.
			end := 0
			if n := len(ti.clusters); n > 0 {
				end = ti.clusters[n-1].box.Max.X
			}
			ti.clusters = append(ti.clusters, cluster{start: i, end: i + 1, box: image.Rect(end, 0, end, 1)})
		} else if runesDisplayed == 0 {
			// Not even one word fits and no newline cut it: wrap without truncating to
			// find the word that does not fit (styledtext's WrapWords case).
			call, ti = t.iterateSpan(gtx, maxWidth, span, false)
			runesDisplayed = ti.runes
			multiLine = runesDisplayed < utf8.RuneCountInString(span.Content)
			endedWithNewline = ti.hasNewline
		}
	}
	return spanResult{
		call: call, width: ti.bounds.Dx(), height: ti.bounds.Dy(), ascent: ti.baseline,
		runes: runesDisplayed, multiLine: multiLine, endedWithNewline: endedWithNewline,
		clusters: ti.clusters,
	}
}

// Layout draws the text, under the selection and over the backgrounds, and takes the
// pointer and keys over its area (see State).
func (t TextStyle) Layout(gtx layout.Context) layout.Dimensions {
	t.State.update(gtx)
	spans := make([]SpanStyle, len(t.Styles))
	copy(spans, t.Styles)
	whole := 0
	for i := range spans {
		spans[i].idx, spans[i].start = i, whole
		whole += len(spans[i].Content)
	}
	var all []byte
	for _, s := range t.Styles {
		all = append(all, s.Content...)
	}

	const lineHeightScale = 1.2
	var (
		lineDims       image.Point
		overallSize    image.Point
		lineShapes     []shape
		lineStartIndex int
		placed         []placedShape
		clusters       []cluster
	)
	textMacro := op.Record(gtx.Ops)
	for i := 0; i < len(spans); i++ {
		span := spans[i]
		maxWidth := gtx.Constraints.Max.X - lineDims.X
		res := t.layoutSpan(gtx, maxWidth, span)
		forceToNextLine := lineDims.X > 0 && res.width > maxWidth
		if !forceToNextLine {
			for k := range res.clusters {
				res.clusters[k].start += span.start
				res.clusters[k].end += span.start
			}
			lineShapes = append(lineShapes, shape{
				offset: image.Point{X: lineDims.X}, size: image.Point{X: res.width, Y: res.height},
				call: res.call, ascent: res.ascent, clusters: res.clusters,
			})
			lineDims.X += res.width
			lineDims.Y = max(lineDims.Y, res.height)
			overallSize.X = max(overallSize.X, lineDims.X)
		}
		if res.multiLine || res.endedWithNewline || i == len(spans)-1 || forceToNextLine {
			last := lineShapes[len(lineShapes)-1]
			lineWidth := last.offset.X + last.size.X
			pad := 0
			if lineWidth < gtx.Constraints.Max.X {
				switch t.Alignment {
				case text.Middle:
					pad = (gtx.Constraints.Max.X - lineWidth) / 2
				case text.End:
					pad = gtx.Constraints.Max.X - lineWidth
				}
			}
			height := int(float32(lineDims.Y) * lineHeightScale)
			for k, sh := range lineShapes {
				sh.offset.X += pad
				sh.offset.Y = overallSize.Y
				st := op.Offset(sh.offset).Push(gtx.Ops)
				sh.call.Add(gtx.Ops)
				st.Pop()
				placed = append(placed, placedShape{shape: sh, span: spans[k+lineStartIndex]})
				// A line's clusters span the whole line's height, so the selection is even.
				for _, c := range sh.clusters {
					c.box = image.Rect(sh.offset.X+c.box.Min.X, overallSize.Y, sh.offset.X+c.box.Max.X, overallSize.Y+height)
					clusters = append(clusters, c)
				}
			}
			lineShapes = lineShapes[:0]
			overallSize.Y += height
			lineDims = image.Point{}
		}
		if res.multiLine && !forceToNextLine {
			lineStartIndex = i + 1
			spans = append(spans, SpanStyle{})
			copy(spans[i+2:], spans[i+1:])
			byteLen := 0
			for range res.runes {
				_, n := utf8.DecodeRuneInString(span.Content[byteLen:])
				byteLen += n
			}
			span.Content = span.Content[byteLen:]
			span.start += byteLen
			spans[i+1] = span
		} else if forceToNextLine {
			lineStartIndex = i
			i--
		} else if res.endedWithNewline {
			lineStartIndex = i + 1
		}
	}
	textCall := textMacro.Stop()
	size := gtx.Constraints.Constrain(overallSize)
	t.State.remember(string(all), clusters, placed)

	// Backgrounds and the selection go under the text, decorations over it.
	for _, p := range placed {
		if p.span.Background.A > 0 {
			r := image.Rectangle{Min: p.offset, Max: p.offset.Add(p.size)}
			paint.FillShape(gtx.Ops, p.span.Background, clip.Rect(r).Op())
		}
	}
	if from, to := t.State.Selection(); from < to {
		for _, c := range clusters {
			if c.start >= from && c.end <= to {
				paint.FillShape(gtx.Ops, t.Selection, clip.Rect(c.box).Op())
			}
		}
	}
	textCall.Add(gtx.Ops)
	for _, p := range placed {
		if !p.span.Underline && !p.span.Strikethrough && p.span.Link == "" {
			continue
		}
		thick := max(1, gtx.Sp(p.span.Size)/14)
		base := p.offset.Y + p.ascent
		var ys []int
		if p.span.Underline {
			ys = append(ys, base+thick)
		}
		if p.span.Strikethrough {
			ys = append(ys, base-gtx.Sp(p.span.Size)*3/10)
		}
		for _, y := range ys {
			r := image.Rect(p.offset.X, y, p.offset.X+p.size.X, y+thick)
			paint.FillShape(gtx.Ops, p.span.Color, clip.Rect(r).Op())
		}
	}
	t.State.area(gtx, size)
	return layout.Dimensions{Size: size, Baseline: firstBaseline(placed, size)}
}

// placedShape is a shape where it was drawn, and the span it draws.
type placedShape struct {
	shape
	span SpanStyle
}

func firstBaseline(placed []placedShape, size image.Point) int {
	if len(placed) == 0 {
		return 0
	}
	return size.Y - (placed[0].offset.Y + placed[0].ascent)
}
