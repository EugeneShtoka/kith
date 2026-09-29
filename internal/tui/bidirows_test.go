package tui

import (
	"strings"
	"testing"
)

// One rule, asserted everywhere it applies: **cut first, reorder second.**
func TestNameCellCutsBeforeReordering(t *testing.T) {
	t.Parallel()

	// A name long enough that the cap has to bite, all of it right-to-left.
	const logical = "אבגדהוזחטיכלמנסעפצקרשת"

	t.Run("the kept half is the start of the name", func(t *testing.T) {
		t.Parallel()

		got := nameCell(logical, 8)
		// What a correct cut keeps: the head of the logical string, reordered.
		want := displayName(truncateLogical(logical, 8))
		if !strings.Contains(got, strings.TrimSuffix(want, "…")) {
			t.Errorf("nameCell = %q, want it to carry the *start* of %q", got, logical)
		}
		// What the old order produced: the tail, because clamping a reordered string
		// keeps the leftmost cells.
		if wrong := reorderedThenCut(logical, 8); got == wrong {
			t.Errorf("nameCell = %q — that is the reorder-then-cut answer, the bug", got)
		}
	})

	t.Run("a short name is reordered and not cut", func(t *testing.T) {
		t.Parallel()

		const short = "שלום"
		got := nameCell(short, 40)
		if got != displayName(short) {
			t.Errorf("nameCell = %q, want %q", got, displayName(short))
		}
		if got == short {
			t.Error("a right-to-left name came back in logical order")
		}
	})

	t.Run("an ASCII name is untouched", func(t *testing.T) {
		t.Parallel()

		if got := nameCell("Alpha", 40); got != "Alpha" {
			t.Errorf("nameCell = %q, want it to leave a left-to-right name alone", got)
		}
	})

	t.Run("a width of zero still yields something", func(t *testing.T) {
		t.Parallel()

		// Callers subtract marks and badges from the row, and a narrow pane can take
		// that below one.
		if got := nameCell(logical, 0); got == "" {
			t.Error("nameCell gave nothing back for a width it had to clamp")
		}
	})
}

// reorderedThenCut is the wrong order, kept here so the test above can assert against
// the actual bug rather than against a guess at what it looked like.
func reorderedThenCut(logical string, width int) string {
	return clamp(displayName(logical), width)
}

// A thread row is drawn through listRow like every other row, and its title reaches the
// screen reordered.
func TestThreadRowLabelIsDrawnReordered(t *testing.T) {
	t.Parallel()

	const title = "שיחה על המשלוח"
	if got := nameCell(title, 40); got == title {
		t.Fatal("fixture is not actually reordered; the test would prove nothing")
	}
	if got, want := nameCell(title, 40), displayName(title); got != want {
		t.Errorf("nameCell(%q) = %q, want %q", title, got, want)
	}
}
