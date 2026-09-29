package tui

import (
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// visualOrder is reorder with a left-to-right base.
func visualOrder(s string) string { return reorder(s, bidi.LeftToRight) }

// markRanges styles drawn runes whose logical offset falls in ranges.
func markRanges(visual string, offs []int, ranges []domain.Misspelling, wrong, hint ansi.Style) string {
	return styleVisual(visual, offs, spellMarks(ranges, wrong, hint))
}

// reorderMapped is the piece A4 turns on, and the reason it exists is that ANSI cannot
// be put into a line before the bidi reorder: escape bytes are ASCII, read as direction
// L, and rule L2 scatters them through the row.

// bidiCorpus is the set these properties are checked against: pure LTR (the path that
// never reorders), pure RTL, both mixed in both base directions, digits inside RTL (the
// one real case of a second embedding level), brackets (rule L4 mirrors them), and a
// grapheme cluster that must not be taken apart.
var bidiCorpus = []struct {
	name string
	text string
	base bidi.Direction
}{
	{"plain english", "the quick brown fox", bidi.LeftToRight},
	{"plain hebrew", "שלום עולם", bidi.RightToLeft},
	{"english in a hebrew line", draftBidi, bidi.RightToLeft},
	{"hebrew in an english line", "hello שלום there", bidi.LeftToRight},
	{"digits inside hebrew", "יש 42 הודעות", bidi.RightToLeft},
	{"brackets", "שלום (עולם) שלום", bidi.RightToLeft},
	{"emoji cluster", "שלום 🙋‍♀️ מה קורה", bidi.RightToLeft},
	{"leading english", "ok שלום", bidi.RightToLeft},
	{"empty", "", bidi.LeftToRight},
}

// The two must draw the same thing.
func TestReorderMappedDrawsWhatReorderDraws(t *testing.T) {
	t.Parallel()

	for _, c := range bidiCorpus {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			plain := reorder(c.text, c.base)
			mapped, _ := reorderMapped(c.text, c.base)
			if plain != mapped {
				t.Errorf("reorder and reorderMapped disagree:\n  reorder       %q\n  reorderMapped %q", plain, mapped)
			}
		})
	}
}

// Every drawn rune came from exactly one byte of the source, and every byte that
// started a rune was drawn exactly once.
func TestReorderMappedAccountsForEveryRune(t *testing.T) {
	t.Parallel()

	for _, c := range bidiCorpus {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			visual, offs := reorderMapped(c.text, c.base)
			if got, want := len(offs), utf8.RuneCountInString(visual); got != want {
				t.Fatalf("%d offsets for %d drawn runes", got, want)
			}
			want := runeStarts(c.text)
			got := append([]int(nil), offs...)
			sort.Ints(got)
			if len(got) != len(want) {
				t.Fatalf("%d offsets for %d source runes", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("offsets sorted = %v, want every rune start exactly once %v", got, want)
				}
			}
		})
	}
}

// Put every drawn rune back where its offset says it came from and the logical string
// reappears.
func TestReorderMappedRoundTripsToTheLogicalText(t *testing.T) {
	t.Parallel()

	for _, c := range bidiCorpus {
		if strings.ContainsAny(c.text, "()[]{}") {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			visual, offs := reorderMapped(c.text, c.base)
			back := make([]rune, len(offs))
			order := make([]int, 0, len(offs))
			for i, r := range []rune(visual) {
				back[i] = r
				order = append(order, i)
			}
			sort.Slice(order, func(a, b int) bool { return offs[order[a]] < offs[order[b]] })
			var b strings.Builder
			for _, i := range order {
				b.WriteRune(back[i])
			}
			if b.String() != c.text {
				t.Errorf("rebuilt from offsets = %q, want %q", b.String(), c.text)
			}
		})
	}
}

// The property the underline actually depends on: the runes the renderer styles are
// exactly the runes whose logical offset lies inside a range — no more, no fewer, and
// wherever on the row bidi put them.
func TestMarkRangesStylesExactlyTheRangeWhereverItLands(t *testing.T) {
	t.Parallel()

	style := ansi.NewStyle().Underline(true)
	for _, c := range bidiCorpus {
		if c.text == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// A range over the middle third of the text, snapped to rune boundaries —
			// arbitrary, which is the point: it is not chosen to be easy to draw.
			starts := runeStarts(c.text)
			if len(starts) < 3 {
				t.Skip("too short to cut a range out of")
			}
			from, to := starts[len(starts)/3], starts[2*len(starts)/3]
			ranges := []domain.Misspelling{{Start: from, End: to}}

			visual, offs := reorderMapped(c.text, c.base)
			drawn := markRanges(visual, offs, ranges, style, style)
			if stripStyles(drawn) != visual {
				t.Errorf("styling changed the text:\n  %q\n  %q", stripStyles(drawn), visual)
			}
			got := styledOffsets(drawn, offs)
			sort.Ints(got)
			var want []int
			for _, at := range starts {
				if at >= from && at < to {
					want = append(want, at)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("styled %v, want exactly the range's runes %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("styled %v, want exactly the range's runes %v", got, want)
				}
			}
		})
	}
}

// Nothing to mark takes the path the composer has always taken, escapes and all: no
// escapes.
func TestMarkRangesWithNothingToMarkAddsNothing(t *testing.T) {
	t.Parallel()

	visual, offs := reorderMapped("שלום עולם", bidi.RightToLeft)
	if got := markRanges(visual, offs, nil, ansi.NewStyle().Underline(true), ansi.NewStyle().Underline(true)); got != visual {
		t.Errorf("marking nothing produced %q, want %q untouched", got, visual)
	}
}

// A marked word costs one escape pair, not one per letter.
func TestMarkRangesCoalescesNeighbors(t *testing.T) {
	t.Parallel()

	text := draftTypo
	visual, offs := reorderMapped(text, bidi.LeftToRight)
	plain := ansi.NewStyle().Underline(true)
	drawn := markRanges(visual, offs, []domain.Misspelling{{Start: 2, End: 9}}, plain, plain)
	if got := strings.Count(drawn, "\x1b["); got != 2 {
		t.Errorf("%d escape sequences for one marked word, want 2:\n  %q", got, drawn)
	}
}

// runeStarts is the byte offset of every rune in s, ascending.
func runeStarts(s string) []int {
	var out []int
	for i := range s {
		out = append(out, i)
	}
	return out
}

// styledOffsets reads a rendered line back: for each rune that carries an SGR style,
// the logical offset offs says it came from.
func styledOffsets(rendered string, offs []int) []int {
	var out []int
	at, styled := 0, false
	for rendered != "" {
		if strings.HasPrefix(rendered, "\x1b[") {
			end := strings.IndexByte(rendered, 'm')
			if end < 0 {
				break
			}
			params := rendered[2:end]
			styled = params != "" && params != "0"
			rendered = rendered[end+1:]
			continue
		}
		_, size := utf8.DecodeRuneInString(rendered)
		if styled && at < len(offs) {
			out = append(out, offs[at])
		}
		at++
		rendered = rendered[size:]
	}
	return out
}
