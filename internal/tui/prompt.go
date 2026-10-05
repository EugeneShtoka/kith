package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// promptKind is what an open one-line prompt is asking for.
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
	promptTagEntry
	promptLogin // a :login field; its label and help are the field's (login.go)
)

// promptState is the open prompt and what has been typed into it.
type promptState struct {
	kind  promptKind
	input string
	// fresh marks a prefilled value not yet touched: drawn selected, it is replaced by
	// the first text typed and cleared by the first deletion; a caret motion keeps it.
	fresh bool
}

// active reports whether a prompt is open and holding the keyboard.
func (p promptState) active() bool { return p.kind != promptNone }

// label is the prompt's leading text.
//
//nolint:funlen // one case per enum value, kept whole so `exhaustive` checks it
func (p promptState) label() string {
	switch p.kind {
	case promptCommand:
		return ":"
	case promptJoin:
		return "join room: "
	case promptSearchRoom:
		return "search this room: "
	case promptSearchAll:
		return "search all rooms: "
	case promptMentions:
		return "narrow your mentions: "
	case promptFiles:
		return "narrow these files: "
	case promptStarred:
		return "narrow what you starred: "
	case promptTracked:
		return "narrow these mentions: "
	case promptCaught:
		return "narrow what the filters caught: "
	case promptAlias:
		return "name for this person: "
	case promptRoomName:
		return "show this room as: "
	case promptGroupName:
		return "show this group as: "
	case promptThreadName:
		return "call this thread: "
	case promptRuleSound:
		return "sound file (empty for none): "
	case promptRuleName:
		return "call this rule (empty for none): "
	case promptAttach:
		return "send file (path, or path | caption): "
	case promptSetting:
		return "set to: "
	case promptSettingEntry:
		return "entry: "
	case promptInvite:
		return "invite (@user:server): "
	case promptUnban:
		return "unban (@user:server): "
	case promptNewRoom:
		return "call it: "
	case promptJumpBind:
		return "key sequence for it (e.g. g w), empty to unbind: "
	case promptTagName:
		return "tag name: "
	case promptTagEntry:
		return "entry (empty removes it): "
	case promptLogin, promptNone:
		return "" // a login field's label is the field's (loginPromptLabel)
	}
	return ""
}

// openPrompt opens an empty prompt of the given kind.
func (m Model) openPrompt(kind promptKind) Model {
	return m.openPromptWith(kind, "")
}

// openPromptWith opens a prompt prefilled with text, the caret at its end.
func (m Model) openPromptWith(kind promptKind, text string) Model {
	m.prompt = promptState{kind: kind, input: text}
	m.compose.caret = caret{owner: fieldPrompt, at: len(text)}
	return m
}

// closePrompt closes whatever prompt is open.
func (m Model) closePrompt() Model {
	m.prompt = promptState{}
	return m
}

// handlePromptKey drives an open prompt. Text keys type before any binding is
// consulted, so no keymap can make a character unreachable.
func (m Model) handlePromptKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	// The popup is asked first, only while open, so enter still submits otherwise.
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

// promptChanged reacts to the text changing; search prompts re-run per keystroke.
func (m Model) promptChanged(typed string) (Model, tea.Cmd) {
	switch m.prompt.kind {
	case promptSearchRoom, promptSearchAll, promptMentions, promptFiles, promptStarred,
		promptTracked, promptCaught:
		var typedCmd, search tea.Cmd
		m, typedCmd = m.promptTyped(typed)
		m, search = m.runSearch(m.prompt.input)
		return m, tea.Batch(typedCmd, search)
	case promptCommand:
		// The command table narrows as you type; enter takes its first row.
		return m.promptTyped(typed)
	case promptJoin, promptAlias, promptRoomName, promptGroupName, promptThreadName,
		promptRuleSound, promptRuleName, promptAttach, promptSetting, promptSettingEntry, promptInvite,
		promptUnban, promptNewRoom, promptJumpBind, promptTagName, promptTagEntry, promptLogin, promptNone:
		return m, nil
	}
	return m, nil
}

// submitPrompt acts on what was typed and closes the prompt.
func (m Model) submitPrompt() (Model, tea.Cmd) {
	kind, input := m.prompt.kind, m.prompt.input
	m = m.closePrompt()
	switch kind {
	case promptCommand:
		return m.runCommandLine(input)
	case promptJoin:
		return m.submitJoin(input)
	case promptSearchRoom, promptSearchAll, promptMentions, promptFiles, promptStarred,
		promptTracked, promptCaught:
		// Results are already current; submitting hands them the keyboard.
		return m.commitSearch()
	case promptAlias:
		return m.submitAlias(input)
	case promptRoomName:
		return m.submitRoomName(input)
	case promptGroupName:
		return m.submitGroupName(input)
	case promptThreadName:
		return m.submitThreadName(input)
	case promptRuleSound:
		return m.submitRuleSound(input)
	case promptRuleName:
		return m.submitRuleName(input)
	case promptAttach:
		return m.submitAttach(input)
	case promptSetting, promptSettingEntry:
		return m.submitSettingPrompt(kind, input)
	case promptInvite:
		return m.submitInvite(input)
	case promptUnban:
		return m.submitUnban(input)
	case promptNewRoom:
		return m.submitNewRoom(input)
	case promptJumpBind:
		return m.submitJumpBinding(input)
	case promptTagName, promptTagEntry:
		return m.submitTagPrompt(kind, input)
	case promptLogin:
		return m.submitLogin(input)
	case promptNone:
		return m, nil
	}
	return m, nil
}

// submitSettingPrompt is a value typed on a settings row: a setting's, or an entry of
// a list setting.
func (m Model) submitSettingPrompt(kind promptKind, input string) (Model, tea.Cmd) {
	if kind == promptSettingEntry {
		return m.submitSettingEntry(input)
	}
	return m.submitSetting(input)
}

// submitTagPrompt is a value typed in the tag editor: a tag's name, or an entry of one
// of its lists.
func (m Model) submitTagPrompt(kind promptKind, input string) (Model, tea.Cmd) {
	if kind == promptTagEntry {
		return m.submitTagEntry(input)
	}
	return m.submitTagName(input)
}

// cancelPrompt abandons the prompt, undoing whatever it had started to show.
func (m Model) cancelPrompt() (Model, tea.Cmd) {
	kind := m.prompt.kind
	m = m.closePrompt()
	switch kind {
	case promptSearchRoom, promptSearchAll, promptMentions, promptFiles, promptStarred,
		promptTracked, promptCaught:
		return m.closeSearch()
	case promptAlias:
		m.choosing.identity = pendingIdentity{}
		m = m.say("canceled")
		return m, nil
	case promptRoomName, promptGroupName, promptThreadName:
		// Clear captured targets so a later prompt cannot inherit them.
		m.aimedAt.renamingRoom, m.aimedAt.renamingGroup, m.aimedAt.renamingThread = "", "", ""
		return m, nil
	case promptRuleSound, promptRuleName:
		m.aimedAt.rule, m.aimedAt.ruleScopes = ruleTarget{}, nil
		return m, nil
	case promptSetting, promptSettingEntry:
		return m, nil // the row is as it was; its list stays open
	case promptAttach, promptJoin, promptCommand, promptNone:
		// Nothing captured. (The completion popup takes the first esc itself.)
		return m, nil
	case promptInvite, promptUnban:
		m.aimedAt.member = ""
		return m, nil
	case promptNewRoom:
		m.aimedAt.creating = domain.NewRoom{}
		return m, nil
	case promptJumpBind:
		m.aimedAt.binding, m.aimedAt.bindings = domain.JumpTarget{}, nil
		return m, nil
	case promptTagName:
		if m.choosing.tag.fileRoom != "" { // from the filing picker: back to the room
			m.choosing.tag = tagEditing{}
			return m, nil
		}
		if m.choosing.tag.tag == "" {
			return m.tagsOpen(), nil
		}
		return m.tagOpen(m.choosing.tag.tag), nil
	case promptTagEntry:
		return m.tagEntriesOpen(m.choosing.tag.tag, m.choosing.tag.list), nil
	case promptLogin:
		return m.cancelLogin(), nil
	}
	return m, nil
}
