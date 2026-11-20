package main

import (
	"context"
	"fmt"
	"testing"

	"gioui.org/widget"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// kithd's byte offsets are made rune offsets, so a mark after a Cyrillic word is
// drawn under its own word.
func TestMarksAreRuneOffsets(t *testing.T) {
	text := "привет wrod"
	got := marksOf(text, []domain.Misspelling{{Word: "wrod", Start: 13, End: 17, Suggestions: []string{"word"}}})
	if len(got) != 1 || got[0].start != 7 || got[0].end != 11 {
		t.Fatalf("marks %+v, want wrod at 7..11", got)
	}
	if r := []rune(text); string(r[got[0].start:got[0].end]) != "wrod" {
		t.Errorf("the mark covers %q", string(r[got[0].start:got[0].end]))
	}
}

// The word the caret is in is not marked (it is being typed), nor a kind whose underline
// is "none".
func TestTheWordBeingTypedIsNotMarked(t *testing.T) {
	marks := []mark{{word: "wrod", start: 0, end: 4}, {word: "thw", start: 5, end: 8, rare: true}}
	for _, tc := range []struct {
		caret int
		spell config.DeviceSpell
		want  string
	}{
		{4, config.DeviceSpell{}, "thw"},
		{8, config.DeviceSpell{}, "wrod"},
		{2, config.DeviceSpell{}, "thw"},
		{9, config.DeviceSpell{}, "wrod thw"},
		{9, config.DeviceSpell{RareUnderline: config.SpellNoUnderline}, "wrod"},
		{9, config.DeviceSpell{Underline: config.SpellNoUnderline}, "thw"},
	} {
		var words []string
		for _, m := range underlined(marks, tc.caret, tc.spell) {
			words = append(words, m.word)
		}
		if got := fmt.Sprint(words); got != "["+tc.want+"]" {
			t.Errorf("caret %d %+v: %s, want [%s]", tc.caret, tc.spell, got, tc.want)
		}
	}
}

// A completion is offered for the word the caret ends, three letters in; the ghost is
// the rest of the one clearly leading, in the case typed.
func TestTheGhostIsTheLeaderRest(t *testing.T) {
	if got := wordBeingTyped([]rune("say hel"), 7); got != "hel" {
		t.Errorf("word being typed %q", got)
	}
	if got := wordBeingTyped([]rune("say he"), 6); got != "" {
		t.Errorf("two letters: %q, want none", got)
	}
	if got := wordBeingTyped([]rune("hello"), 2); got != "" {
		t.Errorf("caret inside: %q, want none", got)
	}
	words := []domain.WordCandidate{{Word: "hello", Score: 30}, {Word: "help", Score: 5}}
	if got := ghost("Hel", words, 3); got != "lo" {
		t.Errorf("ghost %q, want lo", got)
	}
	if got := ghost("hel", []domain.WordCandidate{{Word: "hello", Score: 6}, {Word: "helmet", Score: 5}}, 3); got != "" {
		t.Errorf("no clear leader: ghost %q", got)
	}
	if got := ghost("hell", []domain.WordCandidate{{Word: "hello", Score: 30}}, 3); got != "" {
		t.Errorf("saves one letter: ghost %q", got)
	}
}

// Taking a suggestion fixes the word and moves the later marks with it; a skip leaves
// the word and its later copies; the walk ends after the last.
func TestTheWalkFixesAndMovesOn(t *testing.T) {
	u := &ui{spells: map[*widget.Editor]*spelling{}}
	ed := &widget.Editor{}
	ed.SetText("thw wrod thw cta")
	s := u.spellingOf(ed)
	s.checked = ed.Text()
	s.marks = []mark{
		{word: "thw", start: 0, end: 3, suggestions: []string{"the"}},
		{word: "wrod", start: 4, end: 8, suggestions: []string{"word", "world"}},
		{word: "thw", start: 9, end: 12, suggestions: []string{"the"}},
		{word: "cta", start: 13, end: 16, suggestions: []string{"cat"}},
	}
	u.openWalk(ed)
	u.walkStep(context.Background(), ed, s, "spell.skip")
	if s.walk.items[s.walk.at].word != "wrod" || len(s.walk.items) != 3 {
		t.Fatalf("after a skip: at %+v of %d", s.walk.items[s.walk.at], len(s.walk.items))
	}
	u.walkStep(context.Background(), ed, s, "spell.choose_2")
	u.walkStep(context.Background(), ed, s, "spell.choose_1")
	if got := ed.Text(); got != "thw world thw cat" {
		t.Errorf("text %q", got)
	}
	if s.walk != nil {
		t.Errorf("the walk is still open past its last word")
	}
}

// Anything else changing the text (another room's draft) closes the walk, and the
// composer takes typing again.
func TestTheWalkClosesWhenTheTextChanges(t *testing.T) {
	u := &ui{spells: map[*widget.Editor]*spelling{}}
	ed := &widget.Editor{}
	ed.SetText("wrod")
	s := u.spellingOf(ed)
	s.checked, s.marks = ed.Text(), []mark{{word: "wrod", start: 0, end: 4}}
	u.openWalk(ed)
	if s.walk == nil {
		t.Fatal("the walk did not open")
	}
	ed.SetText("another room's draft")
	u.walkKeys(context.Background(), C{}, ed, s, "")
	if s.walk != nil || ed.ReadOnly {
		t.Errorf("walk %v, read-only %v after the text changed", s.walk, ed.ReadOnly)
	}
}
