package tui

import (
	tea "charm.land/bubbletea/v2"
)

type promptKind int

// The prompts. promptNone means none is open.
const (
	promptNone promptKind = iota
	promptJoin
	promptSearchRoom
	promptSearchAll
	promptMentions
	promptFiles
	promptStarred
	promptTracked
	promptCaught
	promptAlias
	promptRoomName
	promptGroupName
	promptThreadName
	promptRuleSound
	promptRuleName
	promptAttach
	promptSetting
	promptSettingEntry
	promptInvite
	promptUnban
	promptNewRoom
	promptJumpBind
	promptCommand
	promptTagName
	promptPersonValue // a phonebook field's value
	promptPersonKey   // a new phonebook field's name
	promptLogin       // a :login field; its label and help are the field's (login.go)
	promptFilterName  // a new filter's name (filteradd.go)
	promptFilterWords // the words it catches
	promptFilterFrom  // the sender it catches, when it has no words
	promptKinds       // how many kinds there are; not a prompt
)

type promptState struct {
	kind  promptKind
	input string
	// fresh marks an untouched prefilled value: drawn selected, replaced by the first typed text
	// and cleared by the first deletion; caret motion keeps it.
	fresh bool
	// hosted marks a prompt opened over a list in the timeline pane (settings, a card, a
	// picker): it is drawn after the list's rows, and the list closes when it is submitted.
	hosted bool
}

func (p promptState) active() bool { return p.kind != promptNone }

func (m Model) openPrompt(kind promptKind) Model {
	return m.openPromptWith(kind, "")
}

// openPromptWith opens a prompt prefilled with text, the caret at its end.
func (m Model) openPromptWith(kind promptKind, text string) Model {
	hosted := m.picker.active() && !m.picker.spec.grid
	m.prompt = promptState{kind: kind, input: text, hosted: hosted}
	m.compose.caret = caret{owner: fieldPrompt, at: len(text)}
	return m
}

// submittedPrompt closes a submitted prompt and the list it was asked over; the handler
// reopens what should follow.
func (m Model) submittedPrompt() Model {
	if m.prompt.hosted {
		m = m.closePicker()
	}
	return m.closePrompt()
}

func (m Model) closePrompt() Model {
	m.prompt = promptState{}
	return m
}

// handlePromptKey drives an open prompt. Text keys type before any binding is consulted, so
// no keymap can make a character unreachable.
func (m Model) handlePromptKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	// The popup gets keys only while open, so enter still submits otherwise.
	if m.completion.active {
		if mdl, cmd, handled := m.handleCompletionKey(key); handled {
			return mdl, cmd
		}
	}
	if text := key.Text; text != "" {
		if m.prompt.fresh {
			m = m.store(fieldPrompt, newEditor(""))
		}
		m.prompt.fresh = false
		m = m.store(fieldPrompt, m.editorFor(fieldPrompt).insert(text))
		return m.promptChanged(text)
	}
	switch m.keys.lookup(key.String(), scopePrompt) {
	case actSubmit:
		return m.submitPrompt()
	case actCancel:
		return m.cancelPrompt()
	case actSearchScope:
		if m.search.active {
			return m.cycleSearchScope()
		}
		if m.prompt.kind == promptJumpBind {
			return m.cycleBindingTarget()
		}
	}
	// Editing keys are not rebindable actions.
	if ed, ok := m.editorFor(fieldPrompt).edit(key, m.keys); ok {
		if m.prompt.fresh && ed.text != m.prompt.input {
			ed = newEditor("") // a deletion takes the whole selected value
		}
		m.prompt.fresh = false
		changed := ed.text != m.prompt.input
		m = m.store(fieldPrompt, ed)
		if !changed {
			// Caret movement is not a new query.
			return m, nil
		}
		return m.promptChanged("")
	}
	return m, nil
}

// promptChanged reacts to the text changing: a search re-runs, the command table
// narrows.
func (m Model) promptChanged(typed string) (Model, tea.Cmd) {
	if typing := promptSpecs[m.prompt.kind].typing; typing != nil {
		return typing(m, typed)
	}
	return m, nil
}

// submitPrompt acts on what was typed and closes the prompt.
func (m Model) submitPrompt() (Model, tea.Cmd) {
	kind, input := m.prompt.kind, m.prompt.input
	m = m.submittedPrompt()
	if submit := promptSpecs[kind].submit; submit != nil {
		return submit(m, input)
	}
	return m, nil
}

// cancelPrompt abandons the prompt, undoing what it had started to show.
func (m Model) cancelPrompt() (Model, tea.Cmd) {
	kind := m.prompt.kind
	m = m.closePrompt()
	if cancel := promptSpecs[kind].cancel; cancel != nil {
		return cancel(m)
	}
	return m, nil // nothing captured
}

// promptInline reports whether the open prompt is drawn after the content that took over
// the timeline pane (a list, the sign-in) rather than in the status line, which then shows
// only its keys. Over the timeline itself a prompt stays in the status line.
func (m Model) promptInline() bool {
	if !m.prompt.active() {
		return false
	}
	return (m.prompt.hosted && m.picker.active()) || (m.prompt.kind == promptLogin && m.login.stage != loginOff)
}
