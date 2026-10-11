package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The correction walk steps through the draft's misspellings with suggestions. The
// list is a snapshot (never renumbered while open), later offsets shift with each
// replacement, and each replacement is one undo step.

// spellWalkRows is how many suggestions the popup lists (keys 1–9).
const spellWalkRows = 9

type spellWalk struct {
	active bool
	// sendWhenDone is set when a send opened the walk ([spell] check_before_send):
	// reaching the end posts the message; esc does not.
	sendWhenDone bool
	// items are the misspellings walked, in byte offsets into the draft.
	items []domain.Misspelling
	at    int
}

func (w spellWalk) current() (domain.Misspelling, bool) {
	if !w.active || w.at < 0 || w.at >= len(w.items) {
		return domain.Misspelling{}, false
	}
	return w.items[w.at], true
}

// openSpellWalk walks spell.shown, not composerMarks: asking for the walk says the
// caret's word is finished too.
func (m Model) openSpellWalk() (Model, tea.Cmd) {
	switch {
	case !m.conf.base.Spell.SpellEnabled():
		return m.say("spell checking is off — see [spell] enabled"), nil
	case m.spell.off:
		return m.say("nothing to check spelling with"), nil
	case strings.TrimSpace(m.compose.input) == "":
		return m.say("nothing written to check"), nil
	case len(m.spell.shown) == 0:
		return m.say("nothing misspelled"), nil
	}
	return m.startSpellWalk(m.spell.shown, false), nil
}

// startSpellWalk opens the walk over marks, closing the completion popup, which would
// take its keys.
func (m Model) startSpellWalk(marks []domain.Misspelling, sendWhenDone bool) Model {
	m = m.closeCompletion()
	m.walk = spellWalk{active: true, sendWhenDone: sendWhenDone, items: slices.Clone(marks)}
	return m
}

// spellGate is [spell] check_before_send: with something misspelled the walk opens
// instead, and the send resumes at its end. held reports the send was taken over.
func (m Model) spellGate() (Model, tea.Cmd, bool) {
	if !m.conf.base.Spell.CheckBeforeSend || !m.conf.base.Spell.SpellEnabled() ||
		m.spell.off || m.spell.passed || m.walk.active {
		return m, nil, false
	}
	if m.spell.applied != m.spell.gen {
		// Edited since the last answer: ask now and resume the send on the answer.
		return m.say("checking spelling…"), m.checkBeforeSendCmd(m.spell.gen, m.compose.input), true
	}
	if len(m.spell.shown) == 0 {
		return m, nil, false
	}
	return m.startSpellWalk(m.spell.shown, true), nil, true
}

func (m Model) checkBeforeSendCmd(gen int, text string) tea.Cmd {
	parent, backend := m.ctx, m.backend
	rareGates := m.conf.base.Spell.RareBeforeSend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, spellTimeout)
		defer cancel()
		found, err := backend.CheckSpelling(ctx, text)
		switch {
		case errors.Is(err, api.ErrSpellUnavailable):
			return spellBeforeSendMsg{gen: gen, text: text, off: true}
		case err != nil:
			return spellBeforeSendMsg{gen: gen, text: text, failed: true}
		}
		if !rareGates {
			found = withoutHints(found)
		}
		return spellBeforeSendMsg{gen: gen, text: text, found: found}
	}
}

type spellBeforeSendMsg struct {
	gen   int
	text  string
	found []domain.Misspelling
	// off (no engine) and failed (no answer) both let the send through.
	off    bool
	failed bool
}

func (m Model) handleSpellBeforeSend(msg spellBeforeSendMsg) (Model, tea.Cmd) {
	if m.compose.input != msg.text {
		return m.say("the draft changed while it was being checked — send again"), nil
	}
	m, checked := m.handleSpellChecked(spellCheckedMsg{
		gen: msg.gen, text: msg.text, found: msg.found, off: msg.off,
	})
	if msg.off || msg.failed || len(m.spell.shown) == 0 {
		m.spell.passed = true
		var submit tea.Cmd
		m, submit = m.submit()
		return m, tea.Batch(checked, submit)
	}
	return m.startSpellWalk(m.spell.shown, true), checked
}

// handleSpellWalkKey handles every key while the walk is up.
func (m Model) handleSpellWalkKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	act := m.keys.lookup(key.String(), scopeSpell)
	if n, ok := suggestionOf(act); ok {
		return m.takeSuggestion(n + 1)
	}
	switch act {
	case actSpellSkip:
		// Skipping is an answer: a walk skipped to the end still sends.
		return m.nextMisspelling()
	case actSpellAdd:
		return m.learnCurrentWord(true)
	case actSpellIgnore:
		return m.learnCurrentWord(false)
	case actDismiss:
		return m.closeSpellWalk(), nil
	}
	return m, nil
}

func (m Model) takeSuggestion(n int) (Model, tea.Cmd) {
	item, ok := m.walk.current()
	if !ok {
		return m.closeSpellWalk(), nil
	}
	shown := min(len(item.Suggestions), spellWalkRows)
	if n > shown {
		return m, nil
	}
	return m.replaceMisspelling(item.Suggestions[n-1])
}

// replaceMisspelling replaces the current word and every later occurrence (not
// skipped earlier ones), then advances.
func (m Model) replaceMisspelling(with string) (Model, tea.Cmd) {
	item, ok := m.walk.current()
	if !ok {
		return m.closeSpellWalk(), nil
	}
	e := m.editorFor(fieldComposer)
	if item.End > len(e.text) || e.text[item.Start:item.End] != item.Word {
		// The range no longer holds its word; replace nothing.
		return m.nextMisspelling()
	}

	// Front to back with a running offset, so the caret ends after the last fix.
	text, caret, delta := e.text, 0, 0
	items := slices.Clone(m.walk.items)
	var same []int
	for i := m.walk.at; i < len(items); i++ {
		r := items[i]
		if i != m.walk.at && r.Word != item.Word {
			items[i].Start, items[i].End = r.Start+delta, r.End+delta
			continue
		}
		start, end := r.Start+delta, r.End+delta
		if end > len(text) || text[start:end] != r.Word {
			items[i].Start, items[i].End = start, end
			continue
		}
		text = text[:start] + with + text[end:]
		caret = start + len(with)
		delta += len(with) - (r.End - r.Start)
		if i != m.walk.at {
			same = append(same, i)
		}
	}
	m = m.store(fieldComposer, e.changed(text, caret, editWhole))

	// The occurrences corrected along the way are answered; removed back to front.
	for _, s := range slices.Backward(same) {
		items = slices.Delete(items, s, s+1)
	}
	m.walk.items = items
	// armSpellCheck sees the changed draft and drops the touched underlines.
	return m.nextMisspelling()
}

// learnCurrentWord teaches the engine the current word, forever (personal dictionary)
// or for the engine's lifetime, and advances without waiting.
func (m Model) learnCurrentWord(forever bool) (Model, tea.Cmd) {
	item, ok := m.walk.current()
	if !ok {
		return m.closeSpellWalk(), nil
	}
	// The draft didn't change, so no check will clear it: drop every occurrence.
	m.spell.shown = withoutWord(m.spell.shown, item.Word)
	backend, word, ctx := m.backend, item.Word, m.ctx
	teach := func() tea.Msg {
		return spellLearnedMsg{word: word, err: backend.LearnWord(ctx, word, forever)}
	}
	if item.Rare && forever {
		// A rare word is already accepted; allow it past the frequency rule instead.
		teach = func() tea.Msg {
			return spellLearnedMsg{word: word, err: backend.AllowRareWord(ctx, word)}
		}
	}
	next, after := m.nextMisspelling()
	return next, tea.Batch(teach, after)
}

func withoutWord(ranges []domain.Misspelling, word string) []domain.Misspelling {
	out := make([]domain.Misspelling, 0, len(ranges))
	for _, r := range ranges {
		if r.Word == word {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type spellLearnedMsg struct {
	word string
	err  error
}

// handleSpellLearned reports a word that could not be learned; success is silent.
func (m Model) handleSpellLearned(msg spellLearnedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m, nil
	}
	// The word is the user's typing: not logged.
	m.logErr(slog.LevelWarn, "add word to dictionary failed", msg.err)
	return m.say(fmt.Sprintf("%q could not be added to the dictionary", msg.word)), nil
}

func (m Model) nextMisspelling() (Model, tea.Cmd) {
	m.walk.at++
	if m.walk.at < len(m.walk.items) {
		return m, nil
	}
	sending := m.walk.sendWhenDone
	m = m.closeSpellWalk()
	if !sending {
		return m, nil
	}
	// Marked passed so a skipped, still underlined word doesn't reopen the walk.
	m.spell.passed = true
	return m.submit()
}

// closeSpellWalk ends the walk, leaving what it didn't reach underlined.
func (m Model) closeSpellWalk() Model {
	m.walk = spellWalk{}
	return m
}

// spellWalkLines renders the popup: the word, the suggestions under their keys
// (spell.choose_N), and the footer.
func (m Model) spellWalkLines(width int) []string {
	item, ok := m.walk.current()
	if !ok {
		return nil
	}
	// nameCell cuts then reorders, so Hebrew words read correctly.
	counter := fmt.Sprintf(" %d/%d", m.walk.at+1, len(m.walk.items))
	head := m.theme.Title.Render(nameCell(item.Word, max(width-len(counter), 1)))
	out := []string{head + m.theme.Muted.Render(counter)}

	if item.Rare {
		out = append(out, m.theme.Muted.Render(clamp("  rarely written — one of these?", width)))
	}
	if len(item.Suggestions) == 0 {
		out = append(out, m.theme.Muted.Render(clamp("  nothing to suggest", width)))
	}
	for i, s := range item.Suggestions {
		if i >= spellWalkRows {
			break
		}
		key := m.keys.keyHint(scopeSpell, actSpellChoose1+action(i))
		row := "  " + key + "  " + nameCell(s, max(width-4-ansi.StringWidth(key), 1))
		out = append(out, m.theme.Row(false, true).Render(clamp(row, width)))
	}
	return append(out, m.theme.Muted.Render(clamp(m.spellWalkHelp(), width)))
}

// spellWalkHelp is the footer, from the keymap. On a rare word "add" means "it's fine"
// (AllowRareWord).
func (m Model) spellWalkHelp() string {
	add := "add"
	if item, ok := m.walk.current(); ok && item.Rare {
		add = "it's fine"
	}
	return fmt.Sprintf("  %s %s  ·  %s ignore  ·  %s skip  ·  %s stop",
		m.keys.keyHint(scopeSpell, actSpellAdd), add,
		m.keys.keyHint(scopeSpell, actSpellIgnore),
		m.keys.keyHint(scopeSpell, actSpellSkip),
		m.keys.keyHint(scopeSpell, actDismiss))
}
