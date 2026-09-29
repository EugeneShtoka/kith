package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// editor is the line editor behind every text field. Text-producing keys type before
// any binding is consulted, and a configured binding beats the built-in editing set
// (ctrl+e is emoji.open in the composer, end-of-line elsewhere).
type editor struct {
	// text is what has been typed.
	text string
	// at is the caret's byte offset, always on a grapheme boundary.
	at int
	// past and future are the undo and redo stacks, oldest first.
	past, future []editSnapshot
	// last is the kind of the change in progress; same-kind changes coalesce.
	last editKind
}

// editSnapshot is the text and caret at one point in the field's history.
type editSnapshot struct {
	text string
	at   int
}

// editKind decides where one undo step ends and the next begins.
type editKind uint8

const (
	editNone editKind = iota
	// editTyping and editDeleting coalesce a run of characters or backspaces.
	editTyping
	editDeleting
	// editWhole (a paste, a word or line kill) is always its own step.
	editWhole
)

// undoDepth bounds the undo history per field.
const undoDepth = 64

// recording pushes the current state as an undo step if this change starts a new one,
// and drops the redo stack, since editing after an undo is a branch.
func (e editor) recording(kind editKind) editor {
	if kind == e.last && kind != editWhole {
		return e // the step in progress continues
	}
	e.past = append(slices.Clone(e.past), editSnapshot{text: e.text, at: e.at})
	if len(e.past) > undoDepth {
		e.past = e.past[len(e.past)-undoDepth:]
	}
	e.future = nil
	e.last = kind
	if kind == editWhole {
		e.last = editNone // and the next change starts a step of its own
	}
	return e
}

// changed records the undo step and sets the new text and caret; every edit goes
// through it so none drops the history.
func (e editor) changed(text string, at int, kind editKind) editor {
	e = e.recording(kind)
	e.text, e.at = text, at
	return e
}

// moved is a caret motion: no step, but it ends the one in progress.
func (e editor) moved(at int) editor {
	e.at, e.last = at, editNone
	return e
}

// undo steps back one change; redo steps forward. ok is false when there is nothing.
func (e editor) undo() (editor, bool) {
	if len(e.past) == 0 {
		return e, false
	}
	prev := e.past[len(e.past)-1]
	e.past = e.past[:len(e.past)-1]
	e.future = append(slices.Clone(e.future), editSnapshot{text: e.text, at: e.at})
	e.text, e.at, e.last = prev.text, prev.at, editNone
	return e, true
}

func (e editor) redo() (editor, bool) {
	if len(e.future) == 0 {
		return e, false
	}
	next := e.future[len(e.future)-1]
	e.future = e.future[:len(e.future)-1]
	e.past = append(slices.Clone(e.past), editSnapshot{text: e.text, at: e.at})
	e.text, e.at, e.last = next.text, next.at, editNone
	return e, true
}

// newEditor is text with the caret at its end.
func newEditor(text string) editor { return editor{text: text, at: len(text)} }

// before and after are the text on either side of the caret.
func (e editor) before() string { return e.text[:e.at] }
func (e editor) after() string  { return e.text[e.at:] }

// insert types s at the caret, leaving the caret after it.
// A single grapheme continues the typing step; anything longer is its own step.
// Whitespace ends the run, so undo takes back a word at a time.
func (e editor) insert(s string) editor {
	if s == "" {
		return e
	}
	kind := editWhole
	if uniseg.GraphemeClusterCount(s) == 1 {
		kind = editTyping
	}
	e = e.changed(e.before()+s+e.after(), e.at+len(s), kind)
	if kind == editTyping && strings.TrimSpace(s) == "" {
		e.last = editNone
	}
	return e
}

// left and right step by one grapheme cluster, in logical order (in RTL text left
// moves toward the start of the sentence, as in vim).
func (e editor) left() editor {
	return e.moved(e.at - lastClusterLen(e.before()))
}

func (e editor) right() editor {
	return e.moved(e.at + firstClusterLen(e.after()))
}

func (e editor) home() editor { return e.moved(0) }
func (e editor) end() editor  { return e.moved(len(e.text)) }

// wordLeft and wordRight step by a word: a run of non-whitespace (readline's rule, so
// ctrl+w takes a whole path or MXID).
func (e editor) wordLeft() editor {
	return e.moved(wordStart(e.before()))
}

func (e editor) wordRight() editor {
	return e.moved(e.at + wordEnd(e.after()))
}

// backspace deletes the grapheme before the caret; deleteForward the one after.
func (e editor) backspace() editor {
	n := lastClusterLen(e.before())
	return e.changed(e.text[:e.at-n]+e.after(), e.at-n, editDeleting)
}

func (e editor) deleteForward() editor {
	n := firstClusterLen(e.after())
	return e.changed(e.before()+e.text[e.at+n:], e.at, editDeleting)
}

func (e editor) deleteWordLeft() editor {
	start := wordStart(e.before())
	return e.changed(e.text[:start]+e.after(), start, editWhole)
}

func (e editor) deleteWordRight() editor {
	return e.changed(e.before()+e.after()[wordEnd(e.after()):], e.at, editWhole)
}

// deleteToStart and deleteToEnd clear one side of the caret.
func (e editor) deleteToStart() editor { return e.changed(e.after(), 0, editWhole) }
func (e editor) deleteToEnd() editor   { return e.changed(e.before(), e.at, editWhole) }

// cleared empties the field as one undo step, for the cut key.
func (e editor) cleared() editor { return e.changed("", 0, editWhole) }

// editActions is what each editing action does (its keys are [keys.edit]). tail marks
// edits that leave the caret at the end: the only ones a picker's filter gets, since
// its arrows walk the list. row_up and row_down are the composer's, not here.
var editActions = map[action]struct {
	tail  bool
	apply func(editor) editor
}{
	actEditLeft:              {false, editor.left},
	actEditRight:             {false, editor.right},
	actEditWordLeft:          {false, editor.wordLeft},
	actEditWordRight:         {false, editor.wordRight},
	actEditStart:             {false, editor.home},
	actEditEnd:               {false, editor.end},
	actEditDeleteBack:        {true, editor.backspace},
	actEditDeleteForward:     {false, editor.deleteForward},
	actEditDeleteWordBack:    {true, editor.deleteWordLeft},
	actEditDeleteWordForward: {false, editor.deleteWordRight},
	actEditDeleteToStart:     {true, editor.deleteToStart},
	actEditDeleteToEnd:       {false, editor.deleteToEnd},
	actEditUndo:              {true, undoEdit},
	actEditRedo:              {true, redoEdit},
}

// undoEdit and redoEdit adapt undo/redo to the table's shape.
func undoEdit(e editor) editor { u, _ := e.undo(); return u }
func redoEdit(e editor) editor { r, _ := e.redo(); return r }

// edit applies an editing key, reporting whether the key was one.
func (e editor) edit(key tea.KeyPressMsg, keys keymap) (editor, bool) {
	return e.applyEdit(keys.lookup(key.String(), scopeEdit), false)
}

// editTail is edit for a field with no visible caret: tail edits only, caret at end.
func (e editor) editTail(key tea.KeyPressMsg, keys keymap) (editor, bool) {
	return e.end().applyEdit(keys.lookup(key.String(), scopeEdit), true)
}

func (e editor) applyEdit(act action, tailOnly bool) (editor, bool) {
	row, ok := editActions[act]
	if !ok || (tailOnly && !row.tail) {
		return e, false
	}
	return row.apply(e), true
}

// field is a text field that can be taking keystrokes, derived from what is on screen
// (Model.typingField), never stored.
type field int

const (
	fieldNone field = iota
	fieldComposer
	fieldReact
	fieldPrompt
	fieldFilter
	// fieldHelpFilter narrows the help overlay; like fieldFilter it has no caret.
	fieldHelpFilter
)

// caret is where the next character lands, and which field owns it. Only one field
// types at a time; a caret owned by another field reads as "at the end".
type caret struct {
	owner field
	at    int
	// goal is the display column up/down motions aim for, plus one so zero means
	// "no goal"; any ordinary edit resets it (see moveComposerRow).
	goal int
}

// typingField is the field currently taking keystrokes, in the order the screen
// shadows them; fieldNone means keys are commands.
func (m Model) typingField() field {
	switch {
	case m.reader.showing(readerHelp) && m.reader.filtering:
		return fieldHelpFilter
	case m.overlayOwnsKeys():
		return fieldNone
	case m.prompt.active():
		return fieldPrompt
	case m.picker.active():
		if m.picker.mode == pickerFilter {
			return fieldFilter
		}
		return fieldNone
	case m.compose.reacting:
		return fieldReact
	case m.compose.insertMode:
		return fieldComposer
	}
	return fieldNone
}

// overlayOwnsKeys reports whether an overlay must be answered before anything else.
func (m Model) overlayOwnsKeys() bool {
	return m.reader.up() || m.verify.active || m.confirm.active()
}

func (m Model) fieldText(f field) string {
	switch f {
	case fieldComposer:
		return m.compose.input
	case fieldReact:
		return m.compose.reactInput
	case fieldPrompt:
		return m.prompt.input
	case fieldFilter:
		return m.picker.filter
	case fieldHelpFilter:
		return m.reader.filter
	case fieldNone:
		return ""
	}
	return ""
}

// editorFor is a field's text with the caret in it, ready to apply an edit to.
func (m Model) editorFor(f field) editor {
	text := m.fieldText(f)
	if m.compose.caret.owner != f {
		// Not this field's caret; its undo history still comes along.
		e := newEditor(text)
		return m.edits[f].into(e)
	}
	return m.edits[f].into(editor{text: text, at: clampOffset(text, m.compose.caret.at)})
}

// editHistory is one field's undo and redo, kept on the Model because the editor is
// rebuilt from the stored text on every keystroke. Indexed by an array (not a map)
// so Model copies do not share it; every write clones before appending.
type editHistory struct {
	past, future []editSnapshot
	last         editKind
}

// into hands a rebuilt editor its history back; historyOf takes it for storing.
func (h editHistory) into(e editor) editor {
	e.past, e.future, e.last = h.past, h.future, h.last
	return e
}

func historyOf(e editor) editHistory {
	return editHistory{past: e.past, future: e.future, last: e.last}
}

// store writes an edited field back, with its caret and history.
func (m Model) store(f field, e editor) Model {
	if f != fieldNone {
		m.edits[f] = historyOf(e)
	}
	switch f {
	case fieldComposer:
		m.compose.input, m.compose.caret = e.text, caret{owner: f, at: e.at}
	case fieldReact:
		m.compose.reactInput, m.compose.caret = e.text, caret{owner: f, at: e.at}
	case fieldPrompt:
		m.prompt.input, m.compose.caret = e.text, caret{owner: f, at: e.at}
	case fieldFilter:
		// The filter does not take the caret, so the emoji browser can insert at the
		// composer's caret underneath.
		m.picker.filter = e.text
		m.picker = m.picker.refilter()
	case fieldHelpFilter:
		// A new filter is a new list: start it from the top.
		m.reader.filter, m.reader.scroll = e.text, 0
	case fieldNone:
	}
	return m
}

// clampOffset keeps a caret inside text and on a grapheme boundary, since the text
// can change under it.
func clampOffset(text string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(text) {
		return len(text)
	}
	last, state := 0, -1
	for rest := text; rest != ""; {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		next := last + len(cluster)
		if next > at {
			return last
		}
		if next == at {
			return at
		}
		last = next
	}
	return last
}

// wordStart is where the word s ends in begins, skipping trailing whitespace.
func wordStart(s string) int {
	i := len(s)
	for i > 0 {
		r, w := utf8.DecodeLastRuneInString(s[:i])
		if !unicode.IsSpace(r) {
			break
		}
		i -= w
	}
	for i > 0 {
		r, w := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= w
	}
	return i
}

// wordEnd is where the word that s starts with ends, skipping leading whitespace.
func wordEnd(s string) int {
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += w
	}
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			break
		}
		i += w
	}
	return i
}

// firstClusterLen and lastClusterLen are the byte lengths of the grapheme clusters
// at each end of s.
func firstClusterLen(s string) int {
	if s == "" {
		return 0
	}
	cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(s, -1)
	return len(cluster)
}

func lastClusterLen(s string) int {
	n, state := 0, -1
	for s != "" {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		n = len(cluster)
	}
	return n
}

// pasteable is pasted text made fit for a field, and how many lines it arrived as.
// Only the composer (multiline) keeps line breaks; elsewhere they become spaces.
// Control characters are dropped so a pasted escape sequence cannot be drawn back
// out as ANSI, and a trailing newline is not counted.
func pasteable(s string, multiline bool) (text string, lines int) {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var b strings.Builder
	b.Grow(len(s))
	lines = 1
	for _, r := range s {
		switch {
		case r == '\n':
			lines++
			if multiline {
				b.WriteByte('\n')
				continue
			}
			b.WriteByte(' ')
		case r == '\t' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), lines
}

// handlePaste puts pasted text into whichever field is taking keystrokes, at the
// caret. With no field open and the timeline focused it starts a message.
func (m Model) handlePaste(content string) (Model, tea.Cmd) {
	target := m.typingField()
	compose := target == fieldNone
	if compose {
		if m.overlayOwnsKeys() || m.picker.active() || m.focus != paneTimeline {
			return m, nil
		}
		target = fieldComposer
	}
	text, lines := pasteable(content, target == fieldComposer)
	if text == "" {
		m = m.say("nothing to paste")
		return m, nil
	}
	if compose {
		m.compose.insertMode = true
	}
	m = m.store(target, m.editorFor(target).insert(text))
	if lines > 1 && target != fieldComposer {
		m = m.say(fmt.Sprintf("pasted %d lines as one — this is a one-line field", lines))
	}
	// Not passed as typed text, so a pasted "@" opens no popup; an open popup still
	// re-reads its query.
	switch target {
	case fieldComposer:
		mdl, cmd := m.composerTyped("")
		return mdl, cmd
	case fieldReact:
		mdl, cmd := m.reactionTyped("")
		return mdl, cmd
	case fieldPrompt:
		return m.promptChanged("")
	case fieldFilter, fieldHelpFilter, fieldNone:
		return m, nil
	}
	return m, nil
}

// segment is one display row's span text[start:end], excluding its newline.
type segment struct{ start, end int }

// wrapSegments lays text out in rows of at most width cells, breaking at a space
// where it can and mid-word where it must. It returns spans rather than a string
// because the caret is a byte offset: every byte stays, a break's space ending its row.
func wrapSegments(text string, width int) []segment {
	if width < 1 {
		width = 1
	}
	segs := make([]segment, 0, 4)
	start, col, lastSpace, state := 0, 0, -1, -1
	for i := 0; i < len(text); {
		if text[i] == '\n' {
			segs = append(segs, segment{start: start, end: i})
			i++
			start, col, lastSpace, state = i, 0, -1, -1
			continue
		}
		cluster, _, _, next := uniseg.FirstGraphemeClusterInString(text[i:], state)
		state = next
		w := clusterWidth(cluster)
		// i > start: a cluster wider than the row must still advance.
		if col+w > width && i > start {
			brk := i
			if lastSpace > start {
				brk = lastSpace // break at the space, keeping it on this row
			}
			segs = append(segs, segment{start: start, end: brk})
			// Rewind to the break so the word carried over is measured on its new row.
			i, start, col, lastSpace, state = brk, brk, 0, -1, -1
			continue
		}
		col += w
		i += len(cluster)
		if cluster == " " {
			lastSpace = i
		}
	}
	return append(segs, segment{start: start, end: len(text)})
}

// caretSegment is the row the caret sits in; at a soft break it picks the later row,
// where the next character lands.
func caretSegment(segs []segment, at int) int {
	for i := len(segs) - 1; i > 0; i-- {
		if at >= segs[i].start {
			return i
		}
	}
	return 0
}

// clusterWidth is one grapheme's width in cells, measured as the renderer measures.
func clusterWidth(cluster string) int { return ansi.StringWidth(cluster) }
