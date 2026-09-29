package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
)

// The palette's pick keys are bindings: 1..9 then 0 by default, rebindable, and the
// legend shows whatever they are.
func TestPaletteKeysAreBindings(t *testing.T) {
	t.Parallel()

	km := newKeymap(config.DefaultKeys())
	for key, want := range map[string]int{"1": 0, "2": 1, "0": 9} {
		if n, ok := pickOf(km.lookup(key, scopeReact)); !ok || n != want {
			t.Errorf("key %s picks %d,%v, want slot %d", key, n, ok, want)
		}
	}
	keys := config.DefaultKeys()
	keys.React.Pick2 = "x"
	m := sized(t, withRooms(t, newModel())).WithKeys(keys)
	m.glyphs.palette = []string{"a", "b", "c"}
	if n, ok := pickOf(m.keys.lookup("x", scopeReact)); !ok || n != 1 {
		t.Errorf("x picks %d,%v, want slot 1", n, ok)
	}
	if _, ok := pickOf(m.keys.lookup("2", scopeReact)); ok {
		t.Error("2 still picks after react.pick_2 moved to x")
	}
	if got := m.paletteHint(); got != "1a xb 3c" {
		t.Errorf("legend = %q, want the live keys", got)
	}
}

// Every grid row has the same width, or the pane's right border breaks.
func TestEmojiGridRowsAreUniform(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.openEmojiPalette()
	m = next

	width := m.width - railWidth - roomsWidth - 2
	rows := m.gridRows(8, width)
	if len(rows) < 2 {
		t.Fatalf("got %d rows, want a grid to check", len(rows))
	}
	want := ansi.StringWidth(rows[0])
	if want != min(m.pickerColumns()*emojiCellWidth, width) {
		t.Errorf("row width = %d, want the full grid width", want)
	}
	for i, row := range rows {
		if got := ansi.StringWidth(row); got != want {
			t.Errorf("row %d is %d wide, row 0 is %d — the border cannot be straight", i, got, want)
		}
	}
}

// The presentation selector is added only to glyphs that measure one column.
func TestEmojiCellSettlesThePresentation(t *testing.T) {
	t.Parallel()

	for _, glyph := range []string{"‼", "✂", "⚠", "❤", "✈", "▶"} {
		cell := emojiCell(glyph)
		if cell == glyph {
			t.Errorf("%q: left as text presentation, so it measures one and draws two", glyph)
		}
		if w := ansi.StringWidth(cell); w != 2 {
			t.Errorf("%q: cell is %d wide, want 2", glyph, w)
		}
	}
	for _, glyph := range []string{"👍", "✅", "⌛", "🔁", "1️⃣", "👨‍👩‍👧"} {
		if cell := emojiCell(glyph); cell != glyph {
			t.Errorf("%q: already two columns, should be left alone (got %q)", glyph, cell)
		}
	}
}

// curated is the built-in vocabulary.
var curated = newEmojiSet(emojiCurated, nil)
