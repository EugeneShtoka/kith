package tui

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/bidi"
)

// The bidi primitives under text.go; nothing else calls them (forbidigo).

// reorder rewrites logical text into the visual order an LTR grid draws, with base
// as the paragraph direction (it fixes where neutrals sit). Pure LTR text is
// returned untouched; glyph shaping is left to the terminal.
func reorder(s string, base bidi.Direction) string {
	texts, levels, _, ok := bidiRuns(s, base)
	if !ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, text := range texts {
		if levels[i]%2 == 1 { // an RTL run reads right-to-left on the grid
			b.WriteString(reverseClusters(text))
			continue
		}
		b.WriteString(text)
	}
	return b.String()
}

// reorderMapped is reorder plus offs[i], the logical byte offset of the i-th drawn
// rune — how styles are applied after the reorder. A logical range crossing an
// RTL/LTR boundary correctly lands as several visual runs. Kept apart from reorder so
// the hot path allocates no offsets; a test asserts both draw the same string.
func reorderMapped(s string, base bidi.Direction) (string, []int) {
	texts, levels, starts, ok := bidiRuns(s, base)
	if !ok {
		return s, runeOffsets(s, 0)
	}
	var b strings.Builder
	b.Grow(len(s))
	offs := make([]int, 0, utf8.RuneCountInString(s))
	for i, text := range texts {
		if levels[i]%2 == 1 {
			drawn, from := reverseClustersMapped(text, starts[i])
			b.WriteString(drawn)
			offs = append(offs, from...)
			continue
		}
		b.WriteString(text)
		offs = append(offs, runeOffsets(text, starts[i])...)
	}
	return b.String(), offs
}

// bidiRuns splits s into bidi runs in drawn order, with each run's embedding level
// and logical start. ok is false when there is nothing to do (no RTL, or x/text
// cannot parse it).
func bidiRuns(s string, base bidi.Direction) (texts []string, levels, starts []int, ok bool) {
	if !containsRTL(s) {
		return nil, nil, nil, false
	}
	var para bidi.Paragraph
	if _, err := para.SetString(s, bidi.DefaultDirection(base)); err != nil {
		return nil, nil, nil, false
	}
	order, err := para.Order()
	if err != nil {
		return nil, nil, nil, false
	}

	// x/text/bidi returns runs in logical order with only a direction; rebuild each
	// level from it and the base (one level of embedding, enough for chat), then
	// apply rule L2.
	baseLevel := 0
	if base == bidi.RightToLeft {
		baseLevel = 1
	}
	n := order.NumRuns()
	texts, levels, starts = make([]string, n), make([]int, n), make([]int, n)
	// Byte offsets are summed here: Run.Pos is a rune index, wrong for non-ASCII.
	at := 0
	for i := range texts {
		run := order.Run(i)
		texts[i] = run.String()
		starts[i] = at
		at += len(texts[i])
		if run.Direction() == bidi.RightToLeft {
			levels[i] = baseLevel | 1 // nearest odd level at/under the base
		} else {
			levels[i] = (baseLevel + 1) &^ 1 // nearest even level above the base
		}
	}
	reverseRunsByLevel(texts, levels, starts)
	return texts, levels, starts, true
}

// runeOffsets is the byte offset of each rune of s, shifted by base.
func runeOffsets(s string, base int) []int {
	out := make([]int, 0, utf8.RuneCountInString(s))
	for i := range s { // ranging a string yields each rune's byte index
		out = append(out, base+i)
	}
	return out
}

// reverseClusters lays an RTL run out right-to-left by grapheme cluster, not rune
// (reversing a ZWJ emoji's runes breaks it and its width). Single-rune clusters go
// through bidi.AppendReverse for bracket mirroring (rule L4).
func reverseClusters(s string) string {
	clusters := make([]string, 0, len(s))
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		c := g.Str()
		if utf8.RuneCountInString(c) == 1 {
			c = string(bidi.AppendReverse(nil, []byte(c)))
		}
		clusters = append(clusters, c)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, cluster := range slices.Backward(clusters) {
		b.WriteString(cluster)
	}
	return b.String()
}

// reverseClustersMapped is reverseClusters carrying each rune's absolute offset
// (base is where s began).
func reverseClustersMapped(s string, base int) (string, []int) {
	type cluster struct {
		text string
		offs []int
	}
	clusters := make([]cluster, 0, len(s))
	at := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		c := g.Str()
		offs := runeOffsets(c, base+at)
		at += len(c)
		if utf8.RuneCountInString(c) == 1 {
			c = string(bidi.AppendReverse(nil, []byte(c)))
		}
		clusters = append(clusters, cluster{text: c, offs: offs})
	}
	var b strings.Builder
	b.Grow(len(s))
	out := make([]int, 0, len(clusters))
	for _, cluster := range slices.Backward(clusters) {
		b.WriteString(cluster.text)
		out = append(out, cluster.offs...)
	}
	return b.String(), out
}

// reverseRunsByLevel applies rule L2 in place: from the highest level down to the
// lowest odd one, reverse each span of runs at that level or higher. The three
// slices are parallel.
func reverseRunsByLevel(texts []string, levels, starts []int) {
	maxLevel, minOdd := 0, int(^uint(0)>>1)
	for _, l := range levels {
		if l > maxLevel {
			maxLevel = l
		}
		if l%2 == 1 && l < minOdd {
			minOdd = l
		}
	}
	for lvl := maxLevel; lvl >= minOdd; lvl-- {
		for i := 0; i < len(levels); {
			if levels[i] < lvl {
				i++
				continue
			}
			j := i
			for j < len(levels) && levels[j] >= lvl {
				j++
			}
			for a, b := i, j-1; a < b; a, b = a+1, b-1 {
				texts[a], texts[b] = texts[b], texts[a]
				levels[a], levels[b] = levels[b], levels[a]
				starts[a], starts[b] = starts[b], starts[a]
			}
			i = j
		}
	}
}

// isRTLRune reports whether r belongs to a right-to-left script.
func isRTLRune(r rune) bool {
	switch {
	case r >= 0x0590 && r <= 0x08FF, // Hebrew, Arabic, Syriac, Thaana, NKo, …
		r >= 0xFB1D && r <= 0xFDFF, // Hebrew & Arabic presentation forms A
		r >= 0xFE70 && r <= 0xFEFF: // Arabic presentation forms B
		return true
	}
	return false
}

// containsRTL reports whether s has any right-to-left character.
func containsRTL(s string) bool {
	for _, r := range s {
		if isRTLRune(r) {
			return true
		}
	}
	return false
}

// paragraphDir is one direction for a whole message: RTL if it has any RTL
// character. Per message, not per line, so wrapped lines do not flip.
func paragraphDir(s string) bidi.Direction {
	if containsRTL(s) {
		return bidi.RightToLeft
	}
	return bidi.LeftToRight
}

// flushRight is the padding that puts a drawn RTL row against the right edge.
func flushRight(vis string, width int) string {
	if pad := width - ansi.StringWidth(vis); pad > 0 {
		return strings.Repeat(" ", pad)
	}
	return ""
}
