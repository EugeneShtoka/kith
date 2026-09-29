package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Composer spell marks. Errors are held in logical byte offsets (resolved after the
// bidi reorder), checked on a pause in typing, moved across edits in between, and
// never drawn on the word the caret is in.

// spellDebounce is how long the composer waits after the last keystroke before asking.
const spellDebounce = 200 * time.Millisecond

// spellTimeout bounds one check from the client's side.
const spellTimeout = 5 * time.Second

// spellState is what the composer knows about its own spelling.
type spellState struct {
	// off is set once the backend has nothing to check with; stop asking.
	off bool
	// shown are the errors being drawn, in logical byte offsets into text.
	shown []domain.Misspelling
	// text is the current draft those offsets describe (edits move them).
	text string
	// gen counts drafts; a stale tick does nothing.
	gen int
	// applied is the generation of the last answer taken, so an older answer that
	// overtakes a newer one is dropped.
	applied int
	// boundary is where a word was just finished (0 for none), consumed by the check
	// it arms so an autocorrection rides that answer. See autocorrect.go.
	boundary int
	// passed says this draft already went through the pre-send walk, so a skipped
	// word does not reopen it. Any edit clears it.
	passed bool
	// explained is set once the first rare-word hint has been named this session.
	explained bool
}

// spellTickMsg is the debounce firing.
type spellTickMsg struct{ gen int }

// spellCheckedMsg is the engine's answer and the draft it is about, which may no
// longer be the draft on screen.
type spellCheckedMsg struct {
	gen   int
	text  string
	found []domain.Misspelling
	// correctAt is the end offset of the just-finished word to autocorrect, 0 for
	// none. By end, not text: the same word can appear twice.
	correctAt int
	// off says there is nothing to check with. It ends the feature for this session.
	off bool
}

// spellTickCmd fires the spell-check debounce.
func spellTickCmd(gen int) tea.Cmd {
	return tea.Tick(spellDebounce, func(time.Time) tea.Msg { return spellTickMsg{gen: gen} })
}

// armSpellCheck notices a changed draft and schedules a check. Called from Update so
// every way the draft changes is caught by one comparison.
func (m Model) armSpellCheck() (Model, tea.Cmd) {
	if m.spell.off || !m.conf.base.Spell.SpellEnabled() {
		return m, nil
	}
	text := m.compose.input
	if text == m.spell.text {
		return m, nil
	}
	m.spell.shown = shiftRanges(m.spell.shown, m.spell.text, text)
	m.spell.text = text
	m.spell.gen++
	m.spell.passed = false
	boundary := m.spell.boundary
	m.spell.boundary = 0
	if strings.TrimSpace(text) == "" {
		m.spell.shown = nil
		return m, nil
	}
	if boundary > 0 {
		// A word was just finished and autocorrect is on: ask now, not after the
		// debounce, so the fix lands while the word is still in view.
		return m, m.checkSpellingCmd(m.spell.gen, text, boundary)
	}
	return m, spellTickCmd(m.spell.gen)
}

// handleSpellTick asks, if the draft it was armed for is still the draft.
func (m Model) handleSpellTick(msg spellTickMsg) (Model, tea.Cmd) {
	if m.spell.off || msg.gen != m.spell.gen {
		return m, nil
	}
	return m, m.checkSpellingCmd(msg.gen, m.spell.text, 0)
}

// checkSpellingCmd asks the backend which words in text are wrong; correctAt as in
// spellCheckedMsg.
func (m Model) checkSpellingCmd(gen int, text string, correctAt int) tea.Cmd {
	parent, backend, log := m.ctx, m.backend, m.log
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, spellTimeout)
		defer cancel()
		found, err := backend.CheckSpelling(ctx, text)
		switch {
		case errors.Is(err, api.ErrSpellUnavailable):
			return spellCheckedMsg{gen: gen, off: true}
		case err != nil:
			// Not news (it runs per pause in typing); keep the underlines that are
			// still true. Logged at debug, never with the text.
			log.Debug("spell check failed", "err", err)
			return nil
		}
		return spellCheckedMsg{gen: gen, text: text, found: found, correctAt: correctAt}
	}
}

// withoutHints keeps only what the engine rejected, dropping rare-word hints.
func withoutHints(found []domain.Misspelling) []domain.Misspelling {
	out := make([]domain.Misspelling, 0, len(found))
	for _, f := range found {
		if !f.Rare {
			out = append(out, f)
		}
	}
	return out
}

// handleSpellChecked takes the answer, moved onto the draft as it is now.
func (m Model) handleSpellChecked(msg spellCheckedMsg) (Model, tea.Cmd) {
	if msg.off {
		m.spell.off, m.spell.shown = true, nil
		return m, nil
	}
	if msg.gen < m.spell.applied {
		return m, nil // an older answer overtook a newer one
	}
	m.spell.applied = msg.gen
	m.spell.shown = shiftRanges(msg.found, msg.text, m.spell.text)
	m = m.explainHints()
	if msg.correctAt > 0 {
		return m.autocorrect(msg), nil
	}
	return m, nil
}

// explainHints names the dotted underline the first time one is drawn in a session.
func (m Model) explainHints() Model {
	if m.spell.explained || !m.hinting() {
		return m
	}
	rare := false
	for _, r := range m.spell.shown {
		rare = rare || r.Rare
	}
	if !rare {
		return m
	}
	m.spell.explained = true
	return m.say(fmt.Sprintf(
		"a dotted underline means the word is rare, not wrong — %s to see what else it might be",
		m.keys.keyHint(scopeTimeline, actSpellWalk)))
}

// shiftRanges moves ranges computed against old onto new. The edit is recovered from
// the common prefix and suffix; a range it touched is dropped (it will be re-checked).
func shiftRanges(ranges []domain.Misspelling, old, new string) []domain.Misspelling {
	if len(ranges) == 0 {
		return ranges
	}
	if old == new {
		return wholeWords(ranges, new)
	}
	head := commonPrefix(old, new)
	tail := commonSuffix(old[head:], new[head:])
	// Where the replaced region ends in old, and how much longer new is than old.
	editEnd, delta := len(old)-tail, len(new)-len(old)
	out := make([]domain.Misspelling, 0, len(ranges))
	for _, r := range ranges {
		switch {
		case r.End <= head:
			out = append(out, r) // before the edit: untouched
		case r.Start >= editEnd:
			// After it: the region has moved by delta.
			r.Start, r.End = r.Start+delta, r.End+delta
			out = append(out, r)
		}
	}
	return wholeWords(out, new)
}

// wholeWords drops ranges that no longer cover a whole word (by endsWord), so typing
// `l` after a marked `wor` does not underline `wor` inside `worl` until the next check.
func wholeWords(ranges []domain.Misspelling, text string) []domain.Misspelling {
	out := make([]domain.Misspelling, 0, len(ranges))
	for _, r := range ranges {
		if r.Start < 0 || r.End > len(text) || r.Start >= r.End {
			continue
		}
		if r.Start > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:r.Start])
			if !endsWord(string(before)) {
				continue
			}
		}
		if r.End < len(text) {
			after, _ := utf8.DecodeRuneInString(text[r.End:])
			if !endsWord(string(after)) {
				continue
			}
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// commonPrefix and commonSuffix count shared bytes at each end (callers pass the
// suffix the rest after the prefix, so they never overlap). Bytes, not runes: a split
// rune only shrinks the shared region, the safe direction.
func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func commonSuffix(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return i
}

// composerMarks are the marks the composer should draw, leaving out the word the
// caret is in (unfinished, not misspelled).
func (m Model) composerMarks() []domain.Misspelling {
	errors, hints := m.underlining(), m.hinting()
	if m.spell.off || len(m.spell.shown) == 0 || (!errors && !hints) {
		return nil
	}
	at := m.editorFor(fieldComposer).at
	out := make([]domain.Misspelling, 0, len(m.spell.shown))
	for _, r := range m.spell.shown {
		if (r.Rare && !hints) || (!r.Rare && !errors) {
			continue
		}
		// Inclusive: a caret just after the last letter is still in the word.
		if at >= r.Start && at <= r.End {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil // markedRow's plain path
	}
	return out
}

// underlining reports whether misspellings are drawn ("none" still checks, for the
// walk).
func (m Model) underlining() bool { return m.underlineStyle() != config.SpellNoUnderline }

// hinting reports whether rare words are drawn.
func (m Model) hinting() bool { return m.rareUnderlineStyle() != config.SpellNoUnderline }

// underlineStyle is the configured mark (setup.Validate rejects unknown ones).
func (m Model) underlineStyle() string {
	style, err := setup.SpellUnderline(m.conf.base.Spell.Underline)
	if err != nil {
		return config.SpellCurly
	}
	return style
}

// rareUnderlineStyle is the configured mark for a rare word.
func (m Model) rareUnderlineStyle() string {
	style, err := setup.RareUnderline(m.conf.base.Spell.RareUnderline)
	if err != nil {
		return config.SpellDotted
	}
	return style
}

// spellUnderline maps a configured style onto the terminal's underline kinds.
func spellUnderline(name string) ansi.Underline {
	switch name {
	case config.SpellCurly:
		return ansi.UnderlineCurly
	case config.SpellDotted:
		return ansi.UnderlineDotted
	default:
		return ansi.UnderlineSingle
	}
}

// markedRow draws one composer row with its marks (byte offsets into s) underlined.
func (m Model) markedRow(s string, dir bidi.Direction, marks []domain.Misspelling) string {
	if len(marks) == 0 {
		return drawFragment(s, dir, nil)
	}
	wrong := m.theme.SpellMark(spellUnderline(m.underlineStyle()), false)
	hint := m.theme.SpellMark(spellUnderline(m.rareUnderlineStyle()), true)
	return drawFragment(s, dir, spellMarks(marks, wrong, hint))
}

// spellMarks is the composer's marks as a markFunc.
func spellMarks(ranges []domain.Misspelling, wrong, hint ansi.Style) markFunc {
	return func(off int) (styleKey, ansi.Style, string) {
		switch markAt(off, ranges) {
		case markWrong:
			return styleKey(markWrong), wrong, ""
		case markHint:
			return styleKey(markHint), hint, ""
		case markNone:
		}
		return 0, ansi.Style{}, ""
	}
}

// spellMark is which mark a run carries: wrong, or rare (worth a look).
type spellMark uint8

const (
	markNone spellMark = iota
	markWrong
	markHint
)

// markAt reports which mark covers a logical byte offset, if any.
func markAt(off int, ranges []domain.Misspelling) spellMark {
	for _, r := range ranges {
		if off >= r.Start && off < r.End {
			if r.Rare {
				return markHint
			}
			return markWrong
		}
	}
	return markNone
}

// rangesIn narrows ranges to [from, to) and rebases them; a wrapped word is marked
// on both rows.
func rangesIn(ranges []domain.Misspelling, from, to int) []domain.Misspelling {
	var out []domain.Misspelling
	for _, r := range ranges {
		start, end := max(r.Start, from), min(r.End, to)
		if start >= end {
			continue
		}
		r.Start, r.End = start-from, end-from
		out = append(out, r)
	}
	return out
}
