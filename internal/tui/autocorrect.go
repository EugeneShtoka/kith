package tui

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// noteWordBoundary records that a word was just finished, so the check armSpellCheck
// arms next in this event can carry a correction as well as underlines. Autocorrect
// only rewrites when the fix is not a guess; each fix is its own undo step.
func (m Model) noteWordBoundary(typed string) Model {
	if !m.correcting() || m.spell.off || !m.conf.base.Spell.SpellEnabled() {
		return m
	}
	// A paste is not someone finishing a word: never correct it silently.
	if utf8.RuneCountInString(typed) != 1 || !endsWord(typed) {
		return m
	}
	if at := m.editorFor(fieldComposer).at - len(typed); at > 0 {
		m.spell.boundary = at
	}
	return m
}

// endsWord reports whether typing s finished a word. Apostrophes and hyphens live
// inside words ("don't", "week-end").
func endsWord(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '\'' && r != '-'
}

// autocorrect applies the one certain fix for the word that just ended. The range is
// shifted onto the current draft; an edit that touched the word meanwhile drops it.
func (m Model) autocorrect(msg spellCheckedMsg) Model {
	if !m.correcting() {
		return m
	}
	for _, r := range msg.found {
		if r.End != msg.correctAt {
			continue
		}
		with, certain := m.autocorrection(r)
		if !certain {
			return m
		}
		moved := shiftRanges([]domain.Misspelling{r}, msg.text, m.compose.input)
		if len(moved) != 1 {
			return m
		}
		return m.replaceWord(moved[0], with)
	}
	return m
}

// replaceWord puts with in place of what r covers, as one undo step, carrying the caret
// along if it sits after the word.
func (m Model) replaceWord(r domain.Misspelling, with string) Model {
	e := m.editorFor(fieldComposer)
	if r.End > len(e.text) || e.text[r.Start:r.End] != r.Word {
		return m
	}
	text := e.text[:r.Start] + with + e.text[r.End:]
	at := e.at
	if at >= r.End {
		at += len(with) - (r.End - r.Start)
	}
	return m.store(fieldComposer, e.changed(text, at, editWhole))
}

// correcting reports whether autocorrect may rewrite anything at all.
func (m Model) correcting() bool {
	return m.conf.base.Spell.Corrects(config.AutocorrectMisspellings) ||
		m.conf.base.Spell.Corrects(config.AutocorrectRare)
}

// autocorrection is what to replace a mark with, and whether it is certain enough to do
// unasked. A rare word's certainty is decided by the backend, which has the corpus.
func (m Model) autocorrection(r domain.Misspelling) (string, bool) {
	if r.Rare {
		if !m.conf.base.Spell.Corrects(config.AutocorrectRare) || !r.Certain || len(r.Suggestions) != 1 {
			return "", false
		}
		return r.Suggestions[0], true
	}
	if !m.conf.base.Spell.Corrects(config.AutocorrectMisspellings) {
		return "", false
	}
	return misspellingCorrection(r.Word, r.Suggestions)
}

// misspellingCorrection: a sole suggestion, or a first suggestion one slip away.
func misspellingCorrection(word string, suggestions []string) (string, bool) {
	switch {
	case len(suggestions) == 0:
		// Usually a name: never touched.
		return "", false
	case len(suggestions) == 1:
		return suggestions[0], true
	case oneCertainEdit(word, suggestions[0]):
		return suggestions[0], true
	}
	return "", false
}

// oneCertainEdit reports whether want is word with one keyboard slip undone: a case
// change, a transposition, or a dropped letter. A wrong letter is deliberately not on
// the list ("cet" could be cat, cot, cut or set).
func oneCertainEdit(word, want string) bool {
	if word == want {
		return false
	}
	if strings.EqualFold(word, want) {
		return true // a case change: "i" for "I", "english" for "English"
	}
	w, t := []rune(word), []rune(want)
	return transposed(w, t) || oneLetterShort(w, t)
}

// transposed reports whether want is word with one adjacent pair swapped.
func transposed(word, want []rune) bool {
	if len(word) != len(want) {
		return false
	}
	i := 0
	for i < len(word) && word[i] == want[i] {
		i++
	}
	return i+1 < len(word) && word[i] == want[i+1] && word[i+1] == want[i] &&
		slices.Equal(word[i+2:], want[i+2:])
}

// oneLetterShort reports whether want is word with one missing letter put back. A
// letter too many is deliberately not corrected.
func oneLetterShort(word, want []rune) bool {
	if len(want) != len(word)+1 {
		return false
	}
	at := 0
	for at < len(word) && word[at] == want[at] {
		at++
	}
	return slices.Equal(word[at:], want[at+1:])
}
