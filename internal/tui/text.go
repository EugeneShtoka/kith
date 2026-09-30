package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The one way somebody's words reach the screen. User text is logical (typed order);
// drawing it is always: cut in logical order, pick one direction per paragraph
// (paragraphDir), reorder each drawn row (text_bidi.go), style after the reorder
// (escape bytes are direction L and rule L2 scatters them), flush RTL right.
//
// Entry points; nothing else may reorder (forbidigo in .golangci.yml):
//   - drawBlock — wrapped paragraphs: the timeline, the reader overlays, history.
//   - drawLine — single-line cells: names, pickers, titles, status, search excerpts.
//   - drawFragment — editable fields that split a paragraph at the caret.
//
// Sentences of our own are English, so user text inside one is isolated (U+2068…
// U+2069) and drawLine lays the sentence out LTR with each isolate in its own
// direction. The marks never reach the terminal.

// markFunc styles a byte by its logical offset: a coalescing key (zero is plain), the
// style, and the hyperlink it belongs to ("" for none).
type markFunc func(logical int) (styleKey, ansi.Style, string)

// ellipsis ends a cut line; appended in logical order, it lands where the text went.
const ellipsis = "…"

// isolateOpen and isolateClose bracket user text inside a sentence of ours. See isolate.
const (
	isolateOpen  = "\u2068"
	isolateClose = "\u2069"
)

// stripIsolates drops the isolate marks.
var stripIsolates = strings.NewReplacer(isolateOpen, "", isolateClose, "").Replace

// hasIsolates reports whether text carries isolate marks, and so is a sentence of ours.
func hasIsolates(text string) bool {
	return strings.Contains(text, isolateOpen) || strings.Contains(text, isolateClose)
}

// isolate marks s as user text inside a sentence of ours. Text with no RTL, or that
// already has isolates (they do not nest), is returned as is.
func isolate(s string) string {
	if !containsRTL(s) || strings.Contains(s, isolateOpen) {
		return s
	}
	return isolateOpen + s + isolateClose
}

// quoted is user text in quotation marks inside a sentence of ours, isolated. Not %q:
// it would escape the isolate marks into a literal `\u2068` on screen.
func quoted(s string) string { return `"` + isolate(s) + `"` }

// lineSpec is how a single-line cell is drawn.
type lineSpec struct {
	// width is the most cells the line may take; zero or less means no limit.
	width int
	// align flushes an RTL line against the right edge of width (list columns).
	align bool
	// sentence lays the line out LTR (our words with user text inside). A line with
	// isolates is a sentence regardless.
	sentence bool
	// paint styles the drawn text after the reorder, never the padding. nil is plain.
	paint func(string) string
	// marks styles by logical offset instead (styledRow); ignored for a sentence.
	marks markFunc
}

// drawLine draws one line of logical text: flattened, cut to width in logical order,
// reordered, optionally flushed right. Text already carrying escapes was drawn by us
// and is only cut.
func drawLine(logical string, spec lineSpec) string {
	text := oneLine.Replace(logical)
	if spec.width > 0 {
		text = ansi.Truncate(text, spec.width, ellipsis)
	}
	if strings.ContainsRune(text, escapeByte) {
		return stripIsolates(text)
	}
	vis, dir := visualLine(text, spec.sentence)
	drawn := vis
	switch {
	case spec.marks != nil && !spec.sentence && !hasIsolates(text):
		drawn = styledRow(text, dir, spec.marks)
	case spec.paint != nil:
		drawn = spec.paint(vis)
	}
	if spec.align && spec.width > 0 && dir == bidi.RightToLeft {
		drawn = flushRight(vis, spec.width) + drawn
	}
	return drawn
}

// drawSentence is a sentence of ours drawn uncut.
func drawSentence(s string) string { return drawLine(s, lineSpec{sentence: true}) }

// displayName is a name drawn uncut. A name inside a sentence is isolated instead;
// reordering both would reverse it twice.
func displayName(name string) string { return drawLine(name, lineSpec{}) }

// nameCell caps a logical name to width cells and draws it. Cutting before the
// reorder keeps an RTL name's head, not its tail.
func nameCell(logical string, width int) string {
	return drawLine(logical, lineSpec{width: max(width, 1)})
}

// truncateLogical cuts logical text to width cells with an ellipsis without drawing
// it, for text still going into something that will be drawn.
func truncateLogical(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, ellipsis)
}

// cutLogical is truncateLogical with no ellipsis.
func cutLogical(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "")
}

// truncateDrawn cuts an already-drawn row to width cells with an ellipsis. It never
// reorders.
func truncateDrawn(row string, width int) string {
	return ansi.Truncate(row, max(width, 1), ellipsis)
}

// visualLine is the drawn order of one logical line and its direction. A sentence (or
// a line with isolates) is LTR with each isolate in its own direction; anything else
// is one paragraph in paragraphDir's direction.
func visualLine(text string, sentence bool) (string, bidi.Direction) {
	if !hasIsolates(text) {
		dir := paragraphDir(text)
		if sentence {
			dir = bidi.LeftToRight
		}
		return reorder(text, dir), dir
	}
	var b strings.Builder
	b.Grow(len(text))
	for text != "" {
		before, rest, opened := strings.Cut(text, isolateOpen)
		b.WriteString(reorder(strings.ReplaceAll(before, isolateClose, ""), bidi.LeftToRight))
		if !opened {
			break
		}
		// An isolate a cut left open runs to the end, taking the ellipsis with it.
		inner, after, _ := strings.Cut(rest, isolateClose)
		inner = strings.ReplaceAll(inner, isolateOpen, "")
		b.WriteString(reorder(inner, paragraphDir(inner)))
		text = after
	}
	return b.String(), bidi.LeftToRight
}

// blockSpec is how a paragraph of logical text is drawn as wrapped rows.
type blockSpec struct {
	// width is the wrap measure and the edge RTL rows flush against.
	width int
	// dropBlank leaves out whitespace-only rows, keeping one if none would remain.
	dropBlank bool
	// paint styles a drawn row given its visual text and direction. nil is plain.
	paint func(vis string, dir bidi.Direction) string
	// marks returns the styling for the row spanning logical [from, to); nil means
	// paint the row instead.
	marks func(from, to int) markFunc
}

// drawBlock draws a paragraph of logical text as screen rows: wrap first, then
// reorder per row (rule L2), one direction for the whole paragraph, RTL rows flushed
// right with unstyled padding. Text carrying escapes was drawn by us and is only
// wrapped.
func drawBlock(logical string, spec blockSpec) []string {
	width := max(spec.width, 1)
	segs := strings.Split(ansi.Wrap(logical, width, ""), "\n")
	if strings.ContainsRune(logical, escapeByte) {
		return segs
	}
	if spec.dropBlank {
		kept := segs[:0]
		for _, s := range segs {
			if strings.TrimSpace(s) != "" {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			kept = append(kept, "") // keep one row, so whatever leads it still shows
		}
		segs = kept
	}
	if strings.Contains(logical, isolateOpen) {
		return sentenceBlock(segs, spec)
	}
	dir := paragraphDir(logical)
	// Rows that cannot be located in the text are painted rather than marked wrong.
	var starts []int
	located := false
	if spec.marks != nil {
		starts, located = segmentStarts(logical, segs)
	}
	rows := make([]string, 0, len(segs))
	for i, seg := range segs {
		vis := reorder(seg, dir)
		drawn, marked := "", false
		if located {
			if at := spec.marks(starts[i], starts[i]+len(seg)); at != nil {
				drawn, marked = styledRow(seg, dir, at), true
			}
		}
		if !marked {
			drawn = vis
			if spec.paint != nil {
				drawn = spec.paint(vis, dir)
			}
		}
		if dir == bidi.RightToLeft {
			drawn = flushRight(vis, width) + drawn
		}
		rows = append(rows, drawn)
	}
	return rows
}

// sentenceBlock is drawBlock for a sentence of ours with isolates: every row LTR,
// nothing flushed, no marks.
func sentenceBlock(segs []string, spec blockSpec) []string {
	rows := make([]string, 0, len(segs))
	for _, seg := range segs {
		vis, dir := visualLine(seg, true)
		if spec.paint != nil {
			vis = spec.paint(vis, dir)
		}
		rows = append(rows, vis)
	}
	return rows
}

// drawFragment draws a piece of a paragraph whose direction was already decided — a
// composer row or one side of a caret. at, when not nil, styles by logical offset.
func drawFragment(s string, dir bidi.Direction, at markFunc) string {
	if at == nil {
		return reorder(s, dir)
	}
	return styledRow(s, dir, at)
}

// styledRow draws s reordered for dir, styled by at per logical offset; equal
// adjacent keys share one escape pair.
func styledRow(s string, dir bidi.Direction, at markFunc) string {
	visual, offs := reorderMapped(s, dir)
	return styleVisual(visual, offs, at)
}

// styleVisual styles a reordered string by each rune's logical offset (offs, from
// reorderMapped). A grapheme cluster is judged by its first byte and never split: an
// escape inside one changes its width.
func styleVisual(visual string, offs []int, at markFunc) string {
	var out, run strings.Builder
	out.Grow(len(visual))
	var current styleKey
	var style ansi.Style
	var href string
	flush := func() {
		if run.Len() == 0 {
			return
		}
		// A plain run (key zero) still carries the base style, the sender's tint; an
		// empty style writes no escape.
		text := style.Styled(run.String())
		// OSC 8 wraps the styled run. An unsafe target is not wrapped: a rejected
		// escape is printed and widens the row (domain.SafeHyperlink).
		if href != "" && domain.SafeHyperlink(href) {
			text = ansi.SetHyperlink(href) + text + ansi.ResetHyperlink()
		}
		out.WriteString(text)
		run.Reset()
	}
	i := 0
	g := uniseg.NewGraphemes(visual)
	for g.Next() {
		cluster := g.Str()
		key, st, link := styleKey(0), ansi.Style{}, ""
		if i < len(offs) {
			key, st, link = at(offs[i])
		}
		// The first run takes its style too: current starts at zero, which a plain run
		// shares.
		if key != current || i == 0 {
			flush()
			current, style, href = key, st, link
		}
		run.WriteString(cluster)
		i += utf8.RuneCountInString(cluster)
	}
	flush()
	return out.String()
}
