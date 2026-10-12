package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// promptSpec is everything one prompt kind does: its label, what typing does, what enter
// and esc do, and how the key legend names them.
type promptSpec struct {
	label   string
	labelOf func(Model) string // a label that depends on what is being asked
	typing  func(Model, string) (Model, tea.Cmd)
	submit  func(Model, string) (Model, tea.Cmd)
	cancel  func(Model) (Model, tea.Cmd) // nil: nothing was captured
	// submitWord and cancelWord name enter and esc in the legend; "" is submit and cancel.
	submitWord, cancelWord string
}

// promptSpecs is every prompt kind's spec; a test checks none is missing.
var promptSpecs map[promptKind]promptSpec

// The cancels several prompts share: each drops what its prompt had captured.

func searchPrompt(label string) promptSpec {
	return promptSpec{
		label: label, typing: Model.searchTyped, submitWord: "walk the results",
		submit: func(m Model, _ string) (Model, tea.Cmd) { return m.commitSearch() },
		cancel: Model.closeSearch,
	}
}

func cancelRenaming(m Model) (Model, tea.Cmd) {
	// Clear captured targets so a later prompt cannot inherit them.
	m.aimedAt.renamingRoom, m.aimedAt.renamingGroup, m.aimedAt.renamingThread = "", "", ""
	return m, nil
}

func cancelRule(m Model) (Model, tea.Cmd) {
	m.aimedAt.rule, m.aimedAt.ruleScopes = ruleTarget{}, nil
	return m, nil
}

func cancelMember(m Model) (Model, tea.Cmd) {
	m.aimedAt.member = ""
	return m, nil
}

func cancelToFilters(m Model) (Model, tea.Cmd) { return m.backToFilters(), nil }
func cancelToCard(m Model) (Model, tea.Cmd)    { return m.cancelPerson(), nil }

// settingPrompt is a settings value's or list entry's prompt, submitted by submit.
func settingPrompt(submit func(Model, string) (Model, tea.Cmd)) promptSpec {
	return promptSpec{labelOf: Model.settingPromptLabel, submit: submit, submitWord: "save", cancelWord: "keep it as it was"}
}

func init() {
	promptSpecs = map[promptKind]promptSpec{
		promptNone:       {},
		promptCommand:    {label: ":", typing: Model.promptTyped, submit: Model.runCommandLine},
		promptJoin:       {label: "join room: ", submit: Model.submitJoin, submitWord: "join"},
		promptSearchRoom: searchPrompt("search this room: "),
		promptSearchAll:  searchPrompt("search all rooms: "),
		promptMentions:   searchPrompt("narrow your mentions: "),
		promptFiles:      searchPrompt("narrow these files: "),
		promptStarred:    searchPrompt("narrow what you starred: "),
		promptTracked:    searchPrompt("narrow these mentions: "),
		promptCaught:     searchPrompt("narrow what the filters caught: "),
		promptAlias: {label: "name for this person: ", submit: Model.submitAlias,
			cancel: func(m Model) (Model, tea.Cmd) {
				m.choosing.identity = pendingIdentity{}
				return m.say("canceled"), nil
			}},
		promptRoomName:     {label: "show this room as: ", submit: Model.submitRoomName, cancel: cancelRenaming},
		promptGroupName:    {label: "show this group as: ", submit: Model.submitGroupName, cancel: cancelRenaming},
		promptThreadName:   {label: "call this thread: ", submit: Model.submitThreadName, cancel: cancelRenaming},
		promptRuleSound:    {label: "sound file (empty for none): ", submit: Model.submitRuleSound, cancel: cancelRule},
		promptRuleName:     {label: "call this rule (empty for none): ", submit: Model.submitRuleName, cancel: cancelRule},
		promptAttach:       {label: "send file (path, or path | caption): ", submit: Model.submitAttach},
		promptSetting:      settingPrompt(Model.submitSetting),
		promptSettingEntry: settingPrompt(Model.submitSettingEntry), // its list stays open as it was
		promptInvite:       {label: "invite (@user:server): ", submit: Model.submitInvite, cancel: cancelMember},
		promptUnban:        {label: "unban (@user:server): ", submit: Model.submitUnban, cancel: cancelMember},
		promptNewRoom: {label: "call it: ", submit: Model.submitNewRoom,
			cancel: func(m Model) (Model, tea.Cmd) {
				m.aimedAt.creating = domain.NewRoom{}
				return m, nil
			}},
		promptJumpBind: {labelOf: Model.bindingLabel, submit: Model.submitJumpBinding,
			cancel: func(m Model) (Model, tea.Cmd) {
				m.aimedAt.binding, m.aimedAt.bindings = domain.JumpTarget{}, nil
				return m, nil
			}},
		promptTagName: {label: "tag name: ", submit: Model.submitTagName, cancel: Model.cancelTagName},
		promptTagEntry: {label: "entry (empty removes it): ", submit: Model.submitTagEntry,
			cancel: func(m Model) (Model, tea.Cmd) {
				return m.tagEntriesOpen(m.choosing.tag.tag, m.choosing.tag.list), nil
			}},
		promptPersonValue: {label: "value (commas make a list, empty removes it): ", submit: Model.submitPersonValue, cancel: cancelToCard},
		promptPersonKey:   {label: "field name: ", submit: Model.submitPersonKey, cancel: cancelToCard},
		promptLogin: {labelOf: Model.loginPromptLabel, submit: Model.submitLogin,
			cancel: func(m Model) (Model, tea.Cmd) { return m.cancelLogin(), nil }},
		promptFilterName: {label: "new filter's name: ", submit: Model.submitFilterName, cancel: cancelToFilters, submitWord: "next"},
		promptFilterWords: {label: "words it catches (globs: pay, pay*, *pay*; empty for a sender instead): ",
			submit: Model.submitFilterWords, cancel: cancelToFilters, submitWord: "next"},
		promptFilterFrom: {label: "sender it catches (@user:server): ", submit: Model.submitFilterFrom, cancel: cancelToFilters, submitWord: "next"},
	}
}

// searchTyped re-runs the search with what is typed.
func (m Model) searchTyped(typed string) (Model, tea.Cmd) {
	var typedCmd, search tea.Cmd
	m, typedCmd = m.promptTyped(typed)
	m, search = m.runSearch(m.prompt.input)
	return m, tea.Batch(typedCmd, search)
}

// cancelTagName returns where the tag's name was asked from: the room being filed, the
// tags, or the tag's page.
func (m Model) cancelTagName() (Model, tea.Cmd) {
	switch {
	case m.choosing.tag.fileRoom != "":
		m.choosing.tag = tagEditing{}
		return m, nil
	case m.choosing.tag.tag == "":
		return m.tagsOpen(), nil
	}
	return m.tagOpen(m.choosing.tag.tag), nil
}

// promptLabel is the open prompt's leading text.
func (m Model) promptLabel() string {
	spec := promptSpecs[m.prompt.kind]
	if spec.labelOf != nil {
		return spec.labelOf(m)
	}
	return spec.label
}
