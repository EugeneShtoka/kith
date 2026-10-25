package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pickerAction is what one picker kind does: choose acts on the row enter picks, apply on
// a ticked set, back on esc (nil closes the picker), remove on x (nil: rows stay).
type pickerAction struct {
	choose func(Model, pickerItem) (Model, tea.Cmd)
	apply  func(Model, []string) (Model, tea.Cmd)
	back   func(Model) Model
	remove func(Model, pickerItem) (Model, tea.Cmd, bool)
}

// removeAs adapts a removal that needs no row.
func removeAs(f func(Model) (Model, tea.Cmd)) func(Model, pickerItem) (Model, tea.Cmd, bool) {
	return func(m Model, _ pickerItem) (Model, tea.Cmd, bool) { return answered(f(m)) }
}

// pickerActions is every picker kind's actions; a kind with none closes on enter.
var pickerActions map[pickerKind]pickerAction

// byValue adapts a handler of the chosen row's value; byIndex one of its index.
func byValue(f func(Model, string) (Model, tea.Cmd)) func(Model, pickerItem) (Model, tea.Cmd) {
	return func(m Model, item pickerItem) (Model, tea.Cmd) { return f(m, item.value) }
}

func byIndex(f func(Model, int) (Model, tea.Cmd)) func(Model, pickerItem) (Model, tea.Cmd) {
	return func(m Model, item pickerItem) (Model, tea.Cmd) { return f(m, atoiSafe(item.value)) }
}

// The esc steps of settings: a value, entry or set back to its row; a group up a level.

func backToRow(m Model) Model { return m.settingsIn(m.choosing.settingGroup, m.choosing.setting) }

func backFromGroup(m Model) Model {
	group := m.choosing.settingGroup
	if parent, at, ok := settingsParent(group); ok {
		return m.settingsIn(parent, at)
	}
	return m.settingsTop(group)
}

func init() {
	account := func(m Model, item pickerItem) (Model, tea.Cmd) { return m.chooseAccountPick(m.picker.kind, item.value) }
	filterStep := pickerAction{back: Model.backToFilters}
	pickerActions = map[pickerKind]pickerAction{
		pickerPerson:     {choose: byValue(Model.choosePersonRow), remove: Model.removePersonField},
		pickerPersonKeys: {choose: byValue(Model.choosePersonKey)},
		pickerReaction:   {choose: byValue(Model.reactWithPicked)},
		pickerVote: {choose: func(m Model, item pickerItem) (Model, tea.Cmd) {
			if item.value == voteTakeBack {
				return m.castVote(nil)
			}
			return m.castVote([]string{item.value})
		}},
		pickerVoteMulti: {apply: Model.castVote},
		pickerEmoji:     {choose: byValue(Model.insertPickedEmoji)},
		pickerPeople:    {choose: func(m Model, _ pickerItem) (Model, tea.Cmd) { return m.aliasSelectedPerson() }},
		pickerContext: {choose: func(m Model, item pickerItem) (Model, tea.Cmd) {
			return m.acceptContext(m.picker.then, item.value)
		}},
		pickerThread: {choose: func(m Model, item pickerItem) (Model, tea.Cmd) {
			return m.openPickedThread(domain.EventID(item.value), item.room)
		}},
		pickerJump:  {choose: byValue(Model.acceptJump)},
		pickerCards: {choose: Model.choosePhonebookCard},
		pickerScheduled: {choose: chooseScheduled, remove: func(m Model, item pickerItem) (Model, tea.Cmd, bool) {
			return answered(cancelScheduled(m, item))
		}},
		pickerIdentity:        {choose: byValue(Model.chooseIdentity)},
		pickerColor:           {choose: byValue(Model.chooseColor)},
		pickerRuleScope:       {choose: byIndex(Model.chooseRuleScope)},
		pickerRuleList:        {choose: byIndex(Model.chooseRuleScope), remove: Model.removeRuleRow},
		pickerSpeedScope:      {choose: byIndex(Model.chooseSpeedScope)},
		pickerRulePreset:      {choose: byValue(Model.chooseRulePreset)},
		pickerSettingGroups:   {choose: byValue(Model.chooseSettingGroup)},
		pickerSetting:         {choose: byValue(Model.chooseSetting), back: backFromGroup, remove: func(m Model, item pickerItem) (Model, tea.Cmd, bool) { return m.removeRecordRow(item.value) }},
		pickerSettingValue:    {choose: byValue(Model.chooseSettingValue), back: backToRow},
		pickerSettingEntries:  {choose: byValue(Model.chooseSettingEntry), back: backToRow, remove: removeAs(Model.removeSettingEntry)},
		pickerSettingSet:      {apply: Model.writeSettingSet, back: backToRow},
		pickerDNDScope:        {choose: byIndex(Model.chooseDNDScope)},
		pickerMuteScope:       {choose: byIndex(Model.chooseDNDScope)},
		pickerDNDFor:          {choose: byValue(Model.chooseDNDFor)},
		pickerNewRoom:         {choose: byValue(Model.chooseNewRoomKind)},
		pickerFilterTag:       {choose: byValue(Model.chooseFilterTag), back: filterStep.back},
		pickerFilterPlaces:    {apply: Model.acceptFilterPlaces, back: filterStep.back},
		pickerFilterMoves:     {apply: Model.acceptFilterMoves, back: filterStep.back},
		pickerCommandTemplate: {choose: byValue(Model.chooseCommandTemplate), back: func(m Model) Model { return m.settingsIn(commandsTable, commandsTable+"#template") }},
		pickerIgnored:         {choose: func(m Model, _ pickerItem) (Model, tea.Cmd) { return m.closePicker(), nil }, remove: Model.removeIgnoredRow},
		pickerPublicRooms:     {choose: Model.chooseJoinPublicRoom},
		pickerChooseAccount:   {choose: byValue(Model.chooseAccount)},
		pickerLoginNetwork:    {choose: account},
		pickerLoginAccount:    {choose: account},
		pickerImportAccount:   {choose: account},
		pickerImportFolders:   {choose: account},
		pickerImportChoice:    {choose: account},
		pickerRoomSpaces:      {apply: Model.applyRoomSpaces},
		pickerNewMembers:      {apply: Model.createNewChat},
		pickerDictionaries:    {apply: Model.acceptDictionaryOffer},
		pickerFrequencies:     {apply: Model.acceptFrequencyOffer},
		pickerCompletionModel: {apply: Model.acceptModelOffer},
		pickerRescan:          {apply: Model.applyRescan},
	}
}

// chooseScheduled retries a refused or failing message now, and cancels any other
// (delete cancels any).
func chooseScheduled(m Model, item pickerItem) (Model, tea.Cmd) {
	if item.detail != "" {
		return m.closePicker(), m.retryScheduledCmd(item.value)
	}
	return cancelScheduled(m, item)
}

// cancelScheduled cancels the queued message chosen, then re-reads the queue so it shows
// what the daemon holds.
func cancelScheduled(m Model, item pickerItem) (Model, tea.Cmd) {
	return m.closePicker(), m.cancelScheduledCmd(item.value)
}
