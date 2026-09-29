package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Autocorrect, whose whole content is the rule that decides when *not* to fire.

// The rule, on its own.
func TestAutocorrectionOnlyFiresOnACertainFix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		word        string
		suggestions []string
		want        string
	}{
		{
			name:        "one suggestion is nothing to choose between",
			word:        typoMisspell,
			suggestions: []string{"misspell"},
			want:        "misspell",
		},
		{
			name:        "a transposition",
			word:        typoThe,
			suggestions: []string{"the", "ten", "tea"},
			want:        "the",
		},
		{
			name:        "a transposition further in",
			word:        typoReceive,
			suggestions: []string{"receive", "relieve", "reprieve"},
			want:        "receive",
		},
		{
			name:        "a case change",
			word:        "english",
			suggestions: []string{"English", "Anglish", "englishes"},
			want:        "English",
		},
		{
			name:        "a letter left out",
			word:        "nedle",
			suggestions: []string{"needle", "noodle", "nestle"},
			want:        "needle",
		},
		{
			name:        "a wrong letter could be several words",
			word:        "cet",
			suggestions: []string{"cat", "cot", "cut", "set"},
			want:        "",
		},
		{
			name:        "a name the engine has never seen",
			word:        "Shtoka",
			suggestions: nil,
			want:        "",
		},
		{
			name:        "two edits away is a guess",
			word:        "recieveing",
			suggestions: []string{"receiving", "receiver"},
			want:        "",
		},
		{
			name:        "a letter too many is not on the list",
			word:        "speeling",
			suggestions: []string{"spelling", "peeling", "spieling"},
			want:        "",
		},
		{
			// Two bytes per letter, so a rule written over bytes would see four
			// differences here and refuse.
			name:        "a transposition in Hebrew is still one swap",
			word:        "כמתב",
			suggestions: []string{"מכתב", "כמבת", "מכבת", "כמת"},
			want:        "מכתב",
		},
		{
			// The swap *is* in the list — second.
			name:        "a swap ranked second is still a choice",
			word:        "עשכיו",
			suggestions: []string{"אשכיו", "עכשיו", "עשיו", "שכיו"},
			want:        "",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, certain := misspellingCorrection(c.word, c.suggestions)
			if c.want == "" {
				if certain {
					t.Errorf("corrected %q to %q; it is a guess, not a slip", c.word, got)
				}
				return
			}
			if !certain || got != c.want {
				t.Errorf("misspellingCorrection(%q) = %q, %v; want %q", c.word, got, certain, c.want)
			}
		})
	}
}

// End to end: typing the space after a misspelled word fixes it, and nothing else
// moves.
func TestFinishingAWordCorrectsIt(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Autocorrect = config.AutocorrectMisspellings
	m = typeInto(t, m, "I "+typoThe)

	// The space that ends the word is what arms the check, and the check is asked about
	// the whole draft with the word's end marked.
	b.found = []domain.Misspelling{wrong("I "+typoThe+" ", typoThe, "the", "ten")}
	next, cmd := press(t, m, keyText(" "))
	m = next
	if cmd == nil {
		t.Fatal("finishing a word asked the backend nothing")
	}
	msg, ok := runTo[spellCheckedMsg](cmd)
	if !ok {
		t.Fatal("no answer came back from the check the space armed")
	}
	if msg.correctAt != len("I "+typoThe) {
		t.Fatalf("the answer marks the word as ending at %d, want %d", msg.correctAt, len("I "+typoThe))
	}
	answered, _ := m.handleSpellChecked(msg)
	m = answered

	if want := "I the "; m.compose.input != want {
		t.Fatalf("the draft is %q, want %q", m.compose.input, want)
	}
	// The caret was after the space and is still after it, so typing carries on where
	// it was rather than jumping to wherever the correction ended.
	if got := m.editorFor(fieldComposer).at; got != len("I the ") {
		t.Errorf("the caret is at %d, want the end of the draft", got)
	}
}

// One ctrl+z gives back exactly what was typed.
func TestAnAutocorrectIsItsOwnUndoStep(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Autocorrect = config.AutocorrectMisspellings
	m = typeInto(t, m, "I "+typoThe+" ")
	typed := m.compose.input

	corrected := m.autocorrect(spellCheckedMsg{
		text:      typed,
		correctAt: len("I " + typoThe),
		found:     []domain.Misspelling{wrong(typed, typoThe, "the", "ten")},
	})
	if corrected.compose.input != "I the " {
		t.Fatalf("the draft is %q, want it corrected", corrected.compose.input)
	}
	e, ok := corrected.editorFor(fieldComposer).undo()
	if !ok {
		t.Fatal("there is nothing to undo after a correction")
	}
	if e.text != typed {
		t.Errorf("one undo left %q, want what was typed: %q", e.text, typed)
	}
}

// Off by default, and off means off: the word is underlined and left alone.
func TestAutocorrectOffLeavesTheWordAlone(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m = typeInto(t, m, "I "+typoThe+" ")
	if m.correcting() {
		t.Fatal("autocorrect is on without anybody asking for it")
	}
	if m.spell.boundary != 0 {
		t.Error("a word boundary was recorded with the feature off")
	}

	typed := m.compose.input
	after := m.autocorrect(spellCheckedMsg{
		text:      typed,
		correctAt: len("I " + typoThe),
		found:     []domain.Misspelling{wrong(typed, typoThe, "the")},
	})
	if after.compose.input != typed {
		t.Errorf("the draft became %q with autocorrect off", after.compose.input)
	}
}

// A paste is not somebody finishing a word.
func TestAPasteIsNotAWordBoundary(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Autocorrect = config.AutocorrectMisspellings
	m = m.store(fieldComposer, m.editorFor(fieldComposer).insert("I "+typoThe+" mail "))

	next, _ := m.composerTyped("I " + typoThe + " mail ")
	if next.spell.boundary != 0 {
		t.Errorf("a pasted paragraph recorded a word boundary at %d", next.spell.boundary)
	}
}

// Mid-word there is nothing to judge.
func TestTypingInsideAWordCorrectsNothing(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Autocorrect = config.AutocorrectMisspellings

	for _, typed := range []string{"a", "'", "-", "4"} {
		next, _ := m.composerTyped(typed)
		if next.spell.boundary != 0 {
			t.Errorf("%q was taken as the end of a word", typed)
		}
	}
}

// A word edited during the round trip is not rewritten under the person editing it.
func TestAWordChangedWhileTheAnswerWasInFlightIsLeftAlone(t *testing.T) {
	t.Parallel()

	b := &spellBackend{}
	m := spellComposing(t, b)
	m.conf.base.Spell.Autocorrect = config.AutocorrectMisspellings
	asked := "I " + typoThe + " "
	m = typeInto(t, m, "I there ") // what the draft says now

	after := m.autocorrect(spellCheckedMsg{
		text:      asked,
		correctAt: len("I " + typoThe),
		found:     []domain.Misspelling{wrong(asked, typoThe, "the")},
	})
	if after.compose.input != "I there " {
		t.Errorf("the draft became %q; the answer was about a draft that had gone", after.compose.input)
	}
}

// runTo runs a command and returns the first message of the wanted type, following a
// batch into its parts.
func runTo[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	if cmd == nil {
		return zero, false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if got, ok := runTo[T](c); ok {
				return got, true
			}
		}
	case T:
		return msg, true
	}
	return zero, false
}

// A rare word is the one case where autocorrect may touch a word the dictionary
// *accepted*, and only on the backend's say-so: Certain means the corpus has never seen
// the word as typed and exactly one commoner word is a slip away.
func TestRareWordsAreOnlyCorrectedWhenTheCorpusIsCertain(t *testing.T) {
	t.Parallel()

	certain := domain.Misspelling{Word: "hte", Suggestions: []string{"the"}, Rare: true, Certain: true}
	tests := []struct {
		name string
		mode config.Autocorrect
		mark domain.Misspelling
		want string
	}{
		{"certain, and rare words are allowed", config.AutocorrectRare, certain, "the"},
		{"certain, under all", config.AutocorrectAll, certain, "the"},
		{
			// The whole safety property: a word the corpus *has* seen is a word
			// somebody writes, and the commoner neighbor is a guess about intent.
			name: "not certain, however lonely the candidate",
			mode: config.AutocorrectAll,
			mark: domain.Misspelling{Word: "tolea", Suggestions: []string{"toleat"}, Rare: true},
			want: "",
		},
		{
			name: "certain but offered a choice, which cannot happen and must not fire",
			mode: config.AutocorrectAll,
			mark: domain.Misspelling{
				Word: "hte", Suggestions: []string{"the", "tie"}, Rare: true, Certain: true,
			},
			want: "",
		},
		{"misspellings only leaves hints alone", config.AutocorrectMisspellings, certain, ""},
		{"off", config.AutocorrectOff, certain, ""},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			m := Model{}
			m.conf.base.Spell.Autocorrect = c.mode
			got, sure := m.autocorrection(c.mark)
			if c.want == "" {
				if sure {
					t.Errorf("rewrote %q to %q", c.mark.Word, got)
				}
				return
			}
			if !sure || got != c.want {
				t.Errorf("autocorrection = %q, %v; want %q", got, sure, c.want)
			}
		})
	}
}

// And the other direction: "rare" alone does not license rewriting a misspelling, which
// is the setting for somebody who trusts the corpus and not the engine's suggestions.
func TestRareOnlyDoesNotCorrectMisspellings(t *testing.T) {
	t.Parallel()

	m := Model{}
	m.conf.base.Spell.Autocorrect = config.AutocorrectRare
	mark := domain.Misspelling{Word: typoReceive, Suggestions: []string{"receive"}}
	if got, sure := m.autocorrection(mark); sure {
		t.Errorf("rewrote a misspelling to %q with autocorrect = rare", got)
	}
}
