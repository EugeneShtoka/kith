package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"
)

// The text module's own contract, one property per case. text_surfaces_test.go is the
// other half: that every surface actually goes through it.

func TestDrawLineCutsInLogicalOrder(t *testing.T) {
	t.Parallel()

	const hebrew = "אבגדה וזחטי"
	got := drawLine(hebrew, lineSpec{width: 5})
	// The first four letters and the ellipsis, reordered: the ellipsis is where the
	// missing text went, which in a right-to-left line is the visual left.
	if want := "…" + reorder("אבגד", bidi.RightToLeft); got != want {
		t.Errorf("drawLine(hebrew, 5) = %q, want %q", got, want)
	}
	if got := drawLine("hello world", lineSpec{width: 6}); got != "hello…" {
		t.Errorf("drawLine(latin, 6) = %q, want the ellipsis on the right", got)
	}
	if got := drawLine("short", lineSpec{width: 20}); got != "short" {
		t.Errorf("a line that fits is not cut, got %q", got)
	}
}

func TestDrawLineNeverSplitsACharacter(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"👨‍👩‍👧👨‍👩‍👧👨‍👩‍👧", // one cluster of seven runes, three times
		"שָׁלוֹם עוֹלָם",  // letters carrying points
		"مرحبا بالعالم",
	} {
		for width := 1; width < 8; width++ {
			got := drawLine(text, lineSpec{width: width})
			if !utf8.ValidString(got) {
				t.Errorf("drawLine(%q, %d) = %q, not valid UTF-8", text, width, got)
			}
			if w := ansi.StringWidth(got); w > width {
				t.Errorf("drawLine(%q, %d) is %d cells wide", text, width, w)
			}
		}
	}
}

func TestDrawLineAlignsRightToLeftWhenAsked(t *testing.T) {
	t.Parallel()

	got := drawLine("שלום", lineSpec{width: 10, align: true})
	if !strings.HasPrefix(got, "      ") || ansi.StringWidth(got) != 10 {
		t.Errorf("aligned = %q, want it flushed against the right edge of 10 cells", got)
	}
	if got := drawLine("hello", lineSpec{width: 10, align: true}); got != "hello" {
		t.Errorf("a left-to-right line is not padded, got %q", got)
	}
	if got := drawLine("שלום", lineSpec{width: 10}); strings.HasPrefix(got, " ") {
		t.Errorf("unaligned = %q, want no padding", got)
	}
}

// A sentence of ours keeps its English in order; each isolated name is reordered in
// its own direction; the marks are gone.
func TestDrawLineLaysOutASentenceLeftToRight(t *testing.T) {
	t.Parallel()

	room, person := "משפחה", "Noa נועה"
	logical := isolate(person) + " in " + isolate(room)
	want := reorder(person, bidi.RightToLeft) + " in " + reorder(room, bidi.RightToLeft)
	if got := drawLine(logical, lineSpec{}); got != want {
		t.Errorf("drawLine(sentence) = %q, want %q", got, want)
	}
	// Without the isolates, asking for a sentence still keeps the English in order.
	if got := drawLine("everyone in "+room, lineSpec{sentence: true}); got != "everyone in "+reorder(room, bidi.RightToLeft) {
		t.Errorf("drawLine(bare sentence) = %q", got)
	}
	// And a cut inside an isolated name leaves the ellipsis with the name it shortened.
	cut := drawLine("in "+isolate("אבגדה"), lineSpec{width: 6})
	if want := "in " + "…" + reorder("אב", bidi.RightToLeft); cut != want {
		t.Errorf("cut sentence = %q, want %q", cut, want)
	}
}

func TestIsolateIsOnlyForRightToLeftAndDoesNotNest(t *testing.T) {
	t.Parallel()

	if got := isolate("Alpha"); got != "Alpha" {
		t.Errorf("isolate(latin) = %q, want it untouched", got)
	}
	once := isolate("שלום")
	if isolate(once) != once {
		t.Errorf("isolate is not idempotent: %q", isolate(once))
	}
	if stripIsolates(once) != "שלום" {
		t.Errorf("stripIsolates(%q) = %q", once, stripIsolates(once))
	}
}

// Text this program already styled is cut and nothing else.
func TestDrawLinePassesStyledTextThrough(t *testing.T) {
	t.Parallel()

	styled := ansi.Style{}.Bold().Styled("שלום")
	if got := drawLine(styled, lineSpec{}); got != styled {
		t.Errorf("drawLine(styled) = %q, want it untouched", got)
	}
}

func TestDrawLineMarksByLogicalOffset(t *testing.T) {
	t.Parallel()

	// The second word is the match. In a right-to-left line it lands on the visual left.
	text := "שלום עולם"
	bold := ansi.Style{}.Bold()
	got := drawLine(text, lineSpec{marks: func(off int) (styleKey, ansi.Style, string) {
		if off >= len("שלום ") {
			return 1, bold, ""
		}
		return 0, ansi.Style{}, ""
	}})
	want := bold.Styled(reorder("עולם", bidi.RightToLeft)) + " " + reorder("שלום", bidi.RightToLeft)
	if got != want {
		t.Errorf("marked = %q, want %q", got, want)
	}
}

func TestDrawBlockWrapsThenReordersAndFlushes(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("שלום עולם ", 4)
	rows := drawBlock(text, blockSpec{width: 20})
	if len(rows) < 2 {
		t.Fatalf("drawBlock = %q, want it wrapped", rows)
	}
	// Reading order: the first row ends (on the right) with the first word.
	if first := strings.TrimRight(rows[0], " "); !strings.HasSuffix(first, reorder("שלום", bidi.RightToLeft)) {
		t.Errorf("first row = %q, want it to end with the first word", rows[0])
	}
	for _, row := range rows {
		if ansi.StringWidth(strings.TrimRight(row, " ")) != 20 {
			t.Errorf("row %q is not flushed to the right edge of 20", row)
		}
	}
	if got := drawBlock("hello world", blockSpec{width: 20}); len(got) != 1 || got[0] != "hello world" {
		t.Errorf("drawBlock(latin) = %q, want it untouched", got)
	}
}

func TestDrawBlockDropsBlankRowsButKeepsOne(t *testing.T) {
	t.Parallel()

	if got := drawBlock("a\n\n   \nb", blockSpec{width: 10, dropBlank: true}); len(got) != 2 {
		t.Errorf("drawBlock = %q, want the blank rows dropped", got)
	}
	if got := drawBlock("\n", blockSpec{width: 10, dropBlank: true}); len(got) != 1 {
		t.Errorf("drawBlock(blank) = %q, want one row kept", got)
	}
	if got := drawBlock("a\n\nb", blockSpec{width: 10}); len(got) != 3 {
		t.Errorf("drawBlock = %q, want the blank row kept when not asked to drop it", got)
	}
}

func TestDrawBlockDrawsASentenceOfOursLeftToRight(t *testing.T) {
	t.Parallel()

	rows := drawBlock("(deleted by "+isolate("דנה")+")", blockSpec{width: 40})
	if len(rows) != 1 || rows[0] != "(deleted by "+reorder("דנה", bidi.RightToLeft)+")" {
		t.Errorf("drawBlock(sentence) = %q", rows)
	}
}

func TestTruncateLogicalKeepsWholeCharacters(t *testing.T) {
	t.Parallel()

	got := truncateLogical("שלום עולם", 4)
	if got != "שלו…" {
		t.Errorf("truncateLogical = %q, want three letters and an ellipsis, still logical", got)
	}
	if got := cutLogical("👨‍👩‍👧x", 2); got != "👨‍👩‍👧" {
		t.Errorf("cutLogical = %q, want the whole emoji", got)
	}
}

// escapedOpen and escapedClose are how %q spells the isolate marks: six printable
// characters each, which is what would reach the screen.
const (
	escapedOpen  = "\\u2068"
	escapedClose = "\\u2069"
)

// A quoted query carries the isolate marks themselves, never an escaped spelling of
// them: `%q` would have written `\u2068` into the sentence, and drawn it on screen.
func TestQuotedKeepsTheIsolateMarksUnescaped(t *testing.T) {
	t.Parallel()

	got := quoted("שלום")
	if got != `"`+isolateOpen+"שלום"+isolateClose+`"` {
		t.Errorf("quoted = %q, want the marks inside the quotes", got)
	}
	if strings.Contains(got, escapedOpen) || strings.Contains(got, escapedClose) {
		t.Errorf("quoted = %q, which spells the marks out as escapes", got)
	}
	if got := quoted("hello"); got != `"hello"` {
		t.Errorf("quoted(latin) = %q", got)
	}

	// And through a real sentence: the picker's header when nothing matches.
	m := withPicker(t, listSpec(false), []pickerItem{{label: "Alice", value: "a"}})
	m.picker.filter = "שלום"
	m.picker = m.picker.refilter()
	header := m.pickerHeader()
	if !strings.Contains(header, isolateOpen+"שלום"+isolateClose) || strings.Contains(header, escapedOpen) {
		t.Errorf("header = %q, want the filter isolated, not escaped", header)
	}
	drawn := stripStyles(strings.Join(m.pickerLines(60, 3), "\n"))
	if !strings.Contains(drawn, `"`+reorder("שלום", bidi.RightToLeft)+`"`) || strings.ContainsAny(drawn, isolateOpen+isolateClose) {
		t.Errorf("drawn header = %q, want the filter quoted in visual order and no marks left", drawn)
	}
}
