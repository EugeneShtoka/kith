package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// offering is a composer at a word boundary with continuations already in.
func offering(t *testing.T, draft string, options ...string) Model {
	t.Helper()
	m, _ := composing(t)
	m = typeInto(t, m, draft)
	m.model.text = draft
	m.model.options = options
	if len(options) > 0 {
		m.model.suggestion = options[0]
	}
	m.model.live = true
	return m
}

func TestANumberKeyTakesTheNthContinuation(t *testing.T) {
	t.Parallel()

	m := offering(t, "the deploy is ", "ready", "still running", "waiting on review")
	next, _, took := m.takeChoice(1)
	if !took {
		t.Fatal("alt+2 was not taken while continuations were on offer")
	}
	if next.compose.input != "the deploy is still running " {
		t.Errorf("composer = %q, want the second continuation and a space after it", next.compose.input)
	}
	// Taking one clears the rest: they answered for a draft that just changed.
	if len(next.model.options) != 0 {
		t.Errorf("options = %v after taking one, want none", next.model.options)
	}
}

// A key past what is on offer falls through.
func TestANumberPastTheOptionsIsNotTaken(t *testing.T) {
	t.Parallel()

	m := offering(t, "the deploy is ", "ready", "still running")
	if _, _, took := m.takeChoice(4); took {
		t.Error("alt+5 was taken with two continuations on offer")
	}
}

// The strip is drawn only for a choice; a single continuation is the ghost's job.
func TestTheStripIsDrawnOnlyForAChoice(t *testing.T) {
	t.Parallel()

	one := offering(t, "the deploy is ", "ready")
	if rows := one.modelOptionLines(60); len(rows) != 0 {
		t.Errorf("a single continuation drew %v, want nothing", rows)
	}

	several := offering(t, "the deploy is ", "ready", "still running", "waiting")
	rows := several.modelOptionLines(60)
	if len(rows) != 1 {
		t.Fatalf("drew %d rows, want one strip", len(rows))
	}
	line := stripStyles(rows[0])
	for _, want := range []string{"1 ready", "2 still running", "3 waiting"} {
		if !strings.Contains(line, want) {
			t.Errorf("strip = %q, want it to carry %q", line, want)
		}
	}
}

// Continuations for an older draft are neither drawn nor takeable.
func TestOptionsForAnOlderDraftAreGone(t *testing.T) {
	t.Parallel()

	m := offering(t, "the deploy is ", "ready", "still running")
	m = typeInto(t, m, "not ")
	if got := m.modelOptions(); len(got) != 0 {
		t.Errorf("options = %v after typing on, want none", got)
	}
	if _, _, took := m.takeChoice(0); took {
		t.Error("a stale continuation was taken")
	}
}

// Mid-word the number keys belong to the word chooser, not the continuations.
func TestTheWordChooserStillOwnsTheKeysMidWord(t *testing.T) {
	t.Parallel()

	m := offering(t, "the deploy is ", "ready", "still running")
	m = typeInto(t, m, "rea")
	m.assist.typed, m.assist.prefix = "rea", "rea"
	m.assist.candidates = []domain.WordCandidate{{Word: "ready", Score: 10}, {Word: "reading", Score: 4}}

	next, _, took := m.takeChoice(1)
	if !took {
		t.Fatal("alt+2 was not taken while a word was being typed")
	}
	if !strings.HasSuffix(next.compose.input, "reading ") {
		t.Errorf("composer = %q, want the second *word* candidate", next.compose.input)
	}
}
