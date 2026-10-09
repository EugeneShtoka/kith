package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// action is something a keypress can ask for. Handlers switch on these rather than
// literal keys, which makes bindings configurable and the help overlay generated.
type action int

// The actions. actNone means "this key does nothing here".
const (
	actNone action = iota
	actQuit
	actInterrupt
	actPaste
	actHelp
	actJumpTo
	actJumpBack
	actJumpForward
	// actBindJump gives the room or space under the cursor a [keys.jump] sequence.
	actFocusNext
	actFocusPrev
	actToggleDND
	actToggleMute
	actUp
	actDown
	actBack
	actOpen
	actPageUp
	actPageDown
	actHalfPageUp
	actHalfPageDown
	actSelectOldest
	actSelectNewest
	actScrollOldest
	actScrollNewest
	actInsert
	actReply
	actGoReply
	actReact
	actRedact
	actEdit
	actNextMention
	actPrevMention
	actNextAttachment
	actPrevAttachment
	actPrevDay
	actNextDay
	actExternalEdit
	actCut
	actSearchScope
	actSend
	actNewline
	actCancel
	actConfirm
	actJoin
	actLeave
	actAccept
	actReject
	actSubmit
	actYes
	actNo
	actYesAll
	actNoAll
	actSearchRoom
	actSearchAll
	actMentions
	actFiles
	actToggleUnreadFirst
	actToggleMentionsFirst
	actToggleDraftsFirst
	actSortRecent
	actSortAlpha
	actJump
	actClose
	actNext
	actPrev
	actAcceptCompletion
	actDismiss
	actOpenEmoji
	actAcceptEmoji
	actCancelEmoji
	actFilter
	// actRenameThread is one action across three scopes; a chord rather than a letter
	// because the thread list is always filtering, so a letter there is text.
	actRenameThread
	actTogglePick
	actIncrease      // the number under the cursor up (settings)
	actDecrease      // and down
	actMoveEntryUp   // the entry under the cursor up a place (a settings list)
	actMoveEntryDown // and down
	actAcceptPick
	actClosePick
	actName
	actShowPeople
	actCopyText
	actCopyURL
	actOpenURL
	actOpenURLFocus
	actFollowLink
	// actRunScript is bound to nothing: it is the intent carried by a chooser opened
	// for a user command whose need has several answers.
	actRunScript
	actTopic
	actStar
	actHistory
	actNotifyRule
	actSettings
	actCommand
	// actRedraw clears and redraws the screen, removing cells left behind when the
	// terminal laid text out differently than measured.
	actRedraw
	actComplete
	actCompleteAll
	actModelPreview
	actCopyCode
	actAttach
	actDownload
	actSaveAs
	actViewMedia
	actVote
	actMarkRead
	actMarkUnread
	actInvite
	// actKick and actBan act on the person under a picker's cursor.
	actKick
	actBan
	actSpaces
	actSpam
	actOpenThread
	actStartThread
	actListThreads
	// actPlay opens a message's media; the rest are live while a voice note is loaded.
	actPlay
	actPlayPause
	actSeekBack
	actSeekForward
	actSlower
	actFaster
	actNormalSpeed
	actSaveSpeed
	actStopPlay
	// Choosing a spelling suggestion is 1–9, a position rather than a binding.
	actReveal
	actSpellWalk
	actSpellSkip
	actSpellAdd
	actSpellIgnore
	// Editing text in a field (scopeEdit), after the field's own actions.
	actEditLeft
	actEditRight
	actEditWordLeft
	actEditWordRight
	actEditStart
	actEditEnd
	actEditDeleteBack
	actEditDeleteForward
	actEditDeleteWordBack
	actEditDeleteWordForward
	actEditDeleteToStart
	actEditDeleteToEnd
	actEditUndo
	actEditRedo
	actEditRowUp
	actEditRowDown
	// Taking the n-th completion option (scopeInsert) and reacting with the n-th
	// palette emoji (scopeReact): consecutive, so the position is the offset.
	actChoose1
	actChoose2
	actChoose3
	actChoose4
	actChoose5
	actPick1
	actPick2
	actPick3
	actPick4
	actPick5
	actPick6
	actPick7
	actPick8
	actPick9
	actPick10
	actSpellChoose1
	actSpellChoose2
	actSpellChoose3
	actSpellChoose4
	actSpellChoose5
	actSpellChoose6
	actSpellChoose7
	actSpellChoose8
	actSpellChoose9
)

// choiceOf is the option position an action takes (0 for the first), if it is one.
func choiceOf(act action) (int, bool) {
	if act >= actChoose1 && act <= actChoose5 {
		return int(act - actChoose1), true
	}
	return 0, false
}

// suggestionOf is the suggestion position an action takes (0 for the first), if it is one.
func suggestionOf(act action) (int, bool) {
	if act >= actSpellChoose1 && act <= actSpellChoose9 {
		return int(act - actSpellChoose1), true
	}
	return 0, false
}

// pickOf is the palette position an action reacts with (0 for the first), if it is one.
func pickOf(act action) (int, bool) {
	if act >= actPick1 && act <= actPick10 {
		return int(act - actPick1), true
	}
	return 0, false
}

// scope is the mode a binding applies in; handlers consult their scopes most
// specific first.
type scope int

// scopeGlobal is live in every mode including text entry, so only keys that produce
// no text belong there; scopeCommand is every mode where you are not typing.
const (
	scopeGlobal scope = iota
	scopeCommand
	scopeNav
	scopeRail
	scopeRooms
	scopeTimeline
	scopeSearch
	scopeInsert
	scopeCompletion
	scopeEmoji
	scopePicker
	scopeReact
	// scopePlayer is consulted ahead of the focused pane while a voice note is loaded.
	scopePlayer
	scopeVerify
	scopeConfirm
	scopePrompt
	// scopeSpell owns the keyboard while the correction walk is up.
	scopeSpell
	// scopeEdit edits the text of whichever field takes typing, after its own scope.
	scopeEdit
)

// scopeTitles name the scopes in the help overlay, in the order it lists them.
var scopeTitles = []struct {
	scope scope
	title string
}{
	{scopeGlobal, "Anywhere"},
	{scopeCommand, "Anywhere but while typing"},
	{scopeNav, "Moving around"},
	{scopeRail, "Spaces rail"},
	{scopeRooms, "Room list"},
	{scopeTimeline, "Timeline — message cursor"},
	{scopeSearch, "Search results"},
	{scopeInsert, "Composing a message"},
	{scopeCompletion, "Completion popup (@… :…)"},
	{scopeSpell, "Correcting a misspelling"},
	{scopeEmoji, "Emoji browser"},
	{scopePicker, "Choosers (people, identities, colors)"},
	{scopeReact, "Reacting"},
	{scopePlayer, "Voice note playing"},
	{scopePrompt, "At a prompt"},
	{scopeConfirm, "Confirming"},
	{scopeVerify, "Device verification"},
	{scopeEdit, "Editing text (in any field)"},
}

// keyActions maps each scope's actions to their [keys] path, in the order help lists
// each scope's rows. The path is how a row finds its keys, its default and its help
// label (config.KeyDoc), and how issue messages name it.
type keyAction struct {
	scope scope
	act   action
	name  string // the config path, "timeline.reply"
}

// label is what the row does, for the help overlay.
func (r keyAction) label() string {
	if label, ok := helpLabelOverrides[r.scope][r.name]; ok {
		return label
	}
	doc, _ := config.KeyDoc(r.name)
	return doc
}

// keys is the row's binding in k.
func (r keyAction) keys(k config.Keys) string {
	value, _ := k.Binding(r.name)
	return value
}

// helpLabelOverrides word a row differently where one binding serves two scopes.
var helpLabelOverrides = map[scope]map[string]string{
	scopeTimeline: {"emoji.open": "react with any emoji (the grid)"},
	scopeInsert:   {"composer.external_edit": "finish this message in $EDITOR"},
	scopePrompt:   {"search.scope": "widen or narrow: room → space → everywhere"},
}

var keyActions = []keyAction{
	{scopeEdit, actEditLeft, "edit.left"},
	{scopeEdit, actEditRight, "edit.right"},
	{scopeEdit, actEditWordLeft, "edit.word_left"},
	{scopeEdit, actEditWordRight, "edit.word_right"},
	{scopeEdit, actEditStart, "edit.start"},
	{scopeEdit, actEditEnd, "edit.end"},
	{scopeEdit, actEditDeleteBack, "edit.delete_back"},
	{scopeEdit, actEditDeleteForward, "edit.delete_forward"},
	{scopeEdit, actEditDeleteWordBack, "edit.delete_word_back"},
	{scopeEdit, actEditDeleteWordForward, "edit.delete_word_forward"},
	{scopeEdit, actEditDeleteToStart, "edit.delete_to_start"},
	{scopeEdit, actEditDeleteToEnd, "edit.delete_to_end"},
	{scopeEdit, actEditUndo, "edit.undo"},
	{scopeEdit, actEditRedo, "edit.redo"},
	{scopeEdit, actEditRowUp, "edit.row_up"},
	{scopeEdit, actEditRowDown, "edit.row_down"},

	{scopeGlobal, actInterrupt, "interrupt"},
	{scopeGlobal, actPaste, "paste"},
	{scopeGlobal, actFocusNext, "focus_next"},
	{scopeGlobal, actFocusPrev, "focus_prev"},
	{scopeGlobal, actToggleDND, "toggle_dnd"},
	{scopeGlobal, actToggleMute, "toggle_mute"},
	{scopeGlobal, actRedraw, "redraw"},
	{scopeGlobal, actJumpTo, "jump_to"},
	{scopeGlobal, actJumpBack, "jump_back"},
	{scopeGlobal, actJumpForward, "jump_forward"},

	{scopeCommand, actHelp, "help"},
	{scopeCommand, actQuit, "quit"},
	{scopeCommand, actSettings, "settings"},
	{scopeCommand, actCommand, "command"},
	{scopeCommand, actSearchRoom, "search.room"},
	{scopeCommand, actSearchAll, "search.all"},
	{scopeCommand, actMentions, "search.mentions"},
	{scopeCommand, actFiles, "search.files"},
	{scopeRooms, actToggleUnreadFirst, "sort.unread_first"},
	{scopeRooms, actToggleMentionsFirst, "sort.mentions_first"},
	{scopeRooms, actToggleDraftsFirst, "sort.drafts_first"},
	{scopeRooms, actSortRecent, "sort.recent"},
	{scopeRooms, actSortAlpha, "sort.alphabetical"},

	{scopeNav, actUp, "nav.up"},
	{scopeNav, actDown, "nav.down"},
	{scopeNav, actOpen, "nav.open"},
	{scopeNav, actBack, "nav.back"},
	{scopeNav, actPageUp, "nav.page_up"},
	{scopeNav, actPageDown, "nav.page_down"},
	{scopeNav, actHalfPageUp, "nav.half_page_up"},
	{scopeNav, actHalfPageDown, "nav.half_page_down"},
	{scopeNav, actSelectOldest, "nav.select_oldest"},
	{scopeNav, actSelectNewest, "nav.select_newest"},
	{scopeNav, actScrollOldest, "nav.scroll_oldest"},
	{scopeNav, actScrollNewest, "nav.scroll_newest"},

	{scopeRail, actName, "rail.name"},
	{scopeRail, actNotifyRule, "rail.notify_rule"},
	{scopeRail, actMarkRead, "rail.mark_read"},
	{scopeRail, actLeave, "rail.leave"},

	{scopeRooms, actAccept, "rooms.accept"},
	{scopeRooms, actReject, "rooms.reject"},
	{scopeRooms, actShowPeople, "rooms.people"},
	{scopeRooms, actViewMedia, "rooms.view_media"},
	{scopeRooms, actRenameThread, "rooms.rename_thread"},
	{scopeRooms, actListThreads, "rooms.list_threads"},
	{scopeRooms, actName, "rooms.name"},
	{scopeRooms, actNotifyRule, "rooms.notify_rule"},
	{scopeRooms, actJoin, "rooms.join"},
	{scopeRooms, actLeave, "rooms.leave"},
	{scopeRooms, actMarkRead, "rooms.mark_read"},
	{scopeRooms, actMarkUnread, "rooms.mark_unread"},
	{scopeRooms, actInvite, "rooms.invite"},
	{scopeRooms, actSpaces, "rooms.spaces"},
	{scopeRooms, actSpam, "rooms.spam"},

	{scopeTimeline, actInsert, "timeline.insert"},
	{scopeTimeline, actOpenEmoji, "emoji.open"},
	{scopeTimeline, actRenameThread, "timeline.rename_thread"},
	{scopeTimeline, actName, "timeline.name"},
	{scopeTimeline, actNotifyRule, "timeline.notify_rule"},
	{scopeTimeline, actCopyText, "timeline.copy_text"},
	{scopeTimeline, actCopyURL, "timeline.copy_url"},
	{scopeTimeline, actOpenURL, "timeline.open_url"},
	{scopeTimeline, actOpenURLFocus, "timeline.open_url_focus"},
	{scopeTimeline, actFollowLink, "timeline.follow_link"},
	{scopeTimeline, actTopic, "timeline.topic"},
	{scopeTimeline, actStar, "timeline.star"},
	{scopeTimeline, actHistory, "timeline.history"},
	{scopeTimeline, actCopyCode, "timeline.copy_code"},
	{scopeTimeline, actDownload, "timeline.download"},
	{scopeTimeline, actSaveAs, "timeline.save_as"},
	{scopeTimeline, actViewMedia, "timeline.view_media"},
	{scopeTimeline, actVote, "timeline.vote"},
	{scopeTimeline, actPlay, "timeline.play"},
	{scopeTimeline, actReply, "timeline.reply"},
	{scopeTimeline, actGoReply, "timeline.go_reply"},
	{scopeTimeline, actRedact, "timeline.redact"},
	{scopeTimeline, actEdit, "timeline.edit"},
	{scopeTimeline, actReact, "timeline.react"},
	{scopeTimeline, actOpenThread, "timeline.open_thread"},
	{scopeTimeline, actStartThread, "timeline.start_thread"},
	{scopeTimeline, actListThreads, "timeline.list_threads"},
	{scopeTimeline, actNextMention, "timeline.next_mention"},
	{scopeTimeline, actPrevMention, "timeline.prev_mention"},
	{scopeTimeline, actNextAttachment, "timeline.next_attachment"},
	{scopeTimeline, actPrevAttachment, "timeline.prev_attachment"},
	{scopeTimeline, actPrevDay, "timeline.prev_day"},
	{scopeTimeline, actNextDay, "timeline.next_day"},
	{scopeTimeline, actExternalEdit, "composer.external_edit"},
	{scopeTimeline, actReveal, "timeline.reveal"},
	{scopeTimeline, actSpellWalk, "spell.open"},

	{scopeCompletion, actNext, "completion.next"},
	{scopeCompletion, actPrev, "completion.prev"},
	{scopeCompletion, actAcceptCompletion, "completion.accept"},
	{scopeCompletion, actDismiss, "completion.dismiss"},

	// 1–9 pick a suggestion and are positions, not rebindable actions.
	{scopeSpell, actSpellSkip, "spell.skip"},
	{scopeSpell, actSpellAdd, "spell.add"},
	{scopeSpell, actSpellIgnore, "spell.ignore"},
	{scopeSpell, actDismiss, "spell.stop"},
	{scopeSpell, actSpellChoose1, "spell.choose_1"},
	{scopeSpell, actSpellChoose2, "spell.choose_2"},
	{scopeSpell, actSpellChoose3, "spell.choose_3"},
	{scopeSpell, actSpellChoose4, "spell.choose_4"},
	{scopeSpell, actSpellChoose5, "spell.choose_5"},
	{scopeSpell, actSpellChoose6, "spell.choose_6"},
	{scopeSpell, actSpellChoose7, "spell.choose_7"},
	{scopeSpell, actSpellChoose8, "spell.choose_8"},
	{scopeSpell, actSpellChoose9, "spell.choose_9"},

	{scopeSearch, actJump, "search.jump"},
	{scopeSearch, actClose, "search.close"},
	{scopeSearch, actSearchScope, "search.scope"},
	{scopeSearch, actStar, "search.star"},
	{scopePrompt, actSearchScope, "search.scope"},

	{scopePicker, actFilter, "picker.filter"},
	{scopePicker, actName, "picker.name"},
	{scopePicker, actKick, "picker.kick"},
	{scopePicker, actBan, "picker.ban"},
	{scopePicker, actRenameThread, "picker.rename"},
	{scopePicker, actTogglePick, "picker.toggle"},
	{scopePicker, actIncrease, "picker.increase"},
	{scopePicker, actDecrease, "picker.decrease"},
	{scopePicker, actMoveEntryUp, "picker.move_up"},
	{scopePicker, actMoveEntryDown, "picker.move_down"},
	{scopePicker, actAcceptPick, "picker.accept"},
	{scopePicker, actClosePick, "picker.close"},

	{scopeEmoji, actAcceptEmoji, "emoji.accept"},
	{scopeEmoji, actCancelEmoji, "emoji.cancel"},

	// send before newline: if a config gives both one key, the earlier row wins it.
	{scopeInsert, actSend, "insert.send"},
	{scopeInsert, actNewline, "insert.newline"},
	{scopeInsert, actOpenEmoji, "emoji.open"},
	{scopeInsert, actExternalEdit, "composer.external_edit"},
	{scopeInsert, actCut, "composer.cut"},
	{scopeInsert, actAttach, "insert.attach"},
	{scopeInsert, actSpellWalk, "spell.open_insert"},
	{scopeInsert, actComplete, "insert.complete"},
	{scopeInsert, actCompleteAll, "insert.complete_all"},
	{scopeInsert, actModelPreview, "insert.model_preview"},
	{scopeInsert, actCancel, "insert.cancel"},
	{scopeInsert, actChoose1, "insert.choose_1"},
	{scopeInsert, actChoose2, "insert.choose_2"},
	{scopeInsert, actChoose3, "insert.choose_3"},
	{scopeInsert, actChoose4, "insert.choose_4"},
	{scopeInsert, actChoose5, "insert.choose_5"},

	{scopePlayer, actPlayPause, "player.play_pause"},
	{scopePlayer, actSeekBack, "player.back"},
	{scopePlayer, actSeekForward, "player.forward"},
	{scopePlayer, actSlower, "player.slower"},
	{scopePlayer, actFaster, "player.faster"},
	{scopePlayer, actNormalSpeed, "player.normal_speed"},
	{scopePlayer, actSaveSpeed, "player.save_speed"},
	{scopePlayer, actStopPlay, "player.stop"},

	{scopeReact, actSend, "react.send"},
	{scopeReact, actCancel, "react.cancel"},
	{scopeReact, actPick1, "react.pick_1"},
	{scopeReact, actPick2, "react.pick_2"},
	{scopeReact, actPick3, "react.pick_3"},
	{scopeReact, actPick4, "react.pick_4"},
	{scopeReact, actPick5, "react.pick_5"},
	{scopeReact, actPick6, "react.pick_6"},
	{scopeReact, actPick7, "react.pick_7"},
	{scopeReact, actPick8, "react.pick_8"},
	{scopeReact, actPick9, "react.pick_9"},
	{scopeReact, actPick10, "react.pick_10"},

	{scopePrompt, actSubmit, "prompt.submit"},
	{scopePrompt, actCancel, "prompt.cancel"},

	{scopeConfirm, actYes, "confirm.yes"},
	{scopeConfirm, actNo, "confirm.no"},
	{scopeConfirm, actYesAll, "confirm.yes_all"},
	{scopeConfirm, actNoAll, "confirm.no_all"},

	{scopeVerify, actConfirm, "verify.confirm"},
	{scopeVerify, actCancel, "verify.cancel"},
}

// essential actions always keep a key, so no keymap can lock you out of moving and
// canceling: one a config leaves with none (unbound with "-", or every key taken) gets
// its default keys back, and the issue says so. Their keys are ordinary defaults, and
// any of them can be given to something else. Quitting is quit's (q) and, if bound,
// interrupt's.
type essentialAction struct {
	scope scope
	act   action
	name  string
}

var essential = []essentialAction{
	{scopeNav, actUp, "nav.up"},
	{scopeNav, actDown, "nav.down"},
	{scopeNav, actBack, "nav.back"},
	{scopeNav, actOpen, "nav.open"},
	{scopeNav, actPageUp, "nav.page_up"},
	{scopeNav, actPageDown, "nav.page_down"},
	{scopeInsert, actCancel, "insert.cancel"},
	{scopeReact, actCancel, "react.cancel"},
	{scopeVerify, actCancel, "verify.cancel"},
	{scopeConfirm, actNo, "confirm.no"},
	{scopePrompt, actCancel, "prompt.cancel"},
	{scopeSearch, actClose, "search.close"},
	{scopeCompletion, actDismiss, "completion.dismiss"},
	{scopeEmoji, actCancelEmoji, "emoji.cancel"},
	{scopePicker, actClosePick, "picker.close"},
}

// keymap resolves a pressed key to an action within a scope. Bindings may be
// sequences ("gg" or "g g"), stored as steps joined by a space. issues lists config
// problems, which are reported rather than fatal.
type keymap struct {
	binds map[scope]map[string]action
	// prefixes maps each incomplete sequence to the scopes that own it, so a press
	// waits for its partner only where the binding lives.
	prefixes map[string]map[scope]bool
	issues   []string
	// labels is each action's keys in resolution order, for help and the legend.
	labels map[scope]map[action][]string
	// jumps and scripts are sequences bound to a place ([keys.jump]) or a user
	// command. They resolve only where keys are commands, after the action scopes, so
	// they can neither swallow text nor shadow a built-in.
	jumps   map[string]domain.JumpTarget
	scripts map[string]string
}

// unbind is the config value that deliberately leaves an action with no key (empty
// means "unset" to fillDefaults).
const unbind = "-"

// withScripts is k with the user-command bindings added, k itself untouched: Models
// are values, and a copy shares k's maps (bindScripts writes into them).
func (k keymap) withScripts(scripts []config.Script) keymap {
	k.issues = slices.Clone(k.issues)
	k.scripts = maps.Clone(k.scripts)
	prefixes := make(map[string]map[scope]bool, len(k.prefixes))
	for seq, scopes := range k.prefixes {
		prefixes[seq] = maps.Clone(scopes)
	}
	k.prefixes = prefixes
	k.bindScripts(scripts)
	return k
}

// keymapFor builds the whole keymap for a config: the action table, then [keys.jump]
// and user-command bindings, refused where they collide with the table.
func keymapFor(cfg config.Config) keymap {
	km := newKeymap(cfg.Keys)
	km.bindScripts(cfg.Commands.Scripts)
	return km
}

// newKeymap builds the action keymap. A bad keymap is never fatal: every problem
// becomes an issue, and an action whose keys are all taken falls back to its
// built-in binding.
func newKeymap(keys config.Keys) keymap {
	km := keymap{
		binds:    map[scope]map[string]action{},
		prefixes: map[string]map[scope]bool{},
		labels:   map[scope]map[action][]string{},
	}
	defaults := config.DefaultKeys()
	for _, row := range keyActions {
		if strings.TrimSpace(row.keys(keys)) == unbind {
			continue // deliberately given no key
		}
		wanted := splitKeys(row.keys(keys))
		usable := make([]string, 0, len(wanted))
		for _, key := range wanted {
			if !sequenceValid(key) {
				km.issues = append(km.issues,
					fmt.Sprintf("%s: %q is not a key name the terminal reports; ignored", row.name, key))
				continue
			}
			usable = append(usable, key)
		}
		for _, key := range usable {
			if why := km.prefixShadow(row.scope, key); why != "" {
				km.issues = append(km.issues, row.name+": "+why)
			}
		}
		bound, clashes := km.bindAll(row.scope, row.act, usable)
		for _, key := range clashes {
			km.issues = append(km.issues, km.clash(row.scope, row.name, key))
		}
		if bound > 0 {
			continue
		}
		fallback := row.keys(defaults)
		if n, _ := km.bindAll(row.scope, row.act, splitKeys(fallback)); n > 0 {
			km.issues = append(km.issues,
				fmt.Sprintf("%s: nothing left to bind, fell back to %q", row.name, fallback))
		} else if !isEssential(row.scope, row.act) { // keepEssentials reports those
			km.issues = append(km.issues, row.name+": no key left to bind it to")
		}
	}

	km.keepEssentials(defaults)
	km.bindJumps(keys.Jump.Map())
	return km
}

// keepEssentials gives each essential action left with no key its default keys back.
// A default another action holds is taken from it, since an essential action outranks
// an ordinary one; the action that lost it falls back to its own default. Both are
// reported.
func (k *keymap) keepEssentials(defaults config.Keys) {
	for _, e := range essential {
		if len(k.keysFor(e.scope, e.act)) > 0 {
			continue
		}
		def, _ := defaults.Binding(e.name)
		if n, _ := k.bindAll(e.scope, e.act, splitKeys(def)); n > 0 {
			k.issues = append(k.issues, fmt.Sprintf(
				"%s: must keep a key, so no keymap can lock you out; using its default %q", e.name, def))
			continue
		}
		key := splitKeys(def)[0]
		holder := k.binds[e.scope][key]
		k.unbindKey(e.scope, key, holder)
		k.bind(e.scope, key, e.act)
		k.issues = append(k.issues, fmt.Sprintf("%s: %q goes back to %s, which must keep a key",
			actionName(e.scope, holder), key, e.name))
		if len(k.keysFor(e.scope, holder)) == 0 {
			fallback, _ := defaults.Binding(actionName(e.scope, holder))
			if n, _ := k.bindAll(e.scope, holder, splitKeys(fallback)); n > 0 {
				k.issues = append(k.issues, fmt.Sprintf("%s: fell back to %q", actionName(e.scope, holder), fallback))
			}
		}
	}
}

// isEssential reports whether act must keep a key in scope s.
func isEssential(s scope, act action) bool {
	return slices.ContainsFunc(essential, func(e essentialAction) bool { return e.scope == s && e.act == act })
}

// unbindKey takes key off act in scope s.
func (k *keymap) unbindKey(s scope, key string, act action) {
	delete(k.binds[s], key)
	k.labels[s][act] = slices.DeleteFunc(slices.Clone(k.labels[s][act]), func(l string) bool { return l == key })
}

// bindJumps records the sequences bound to places. A bad entry costs only that key;
// a sequence an action holds is refused (unbind the action with "-" to take it).
func (k *keymap) bindJumps(jumps map[string]string) {
	if len(jumps) == 0 {
		return
	}
	k.jumps = make(map[string]domain.JumpTarget, len(jumps))
	for _, binding := range slices.Sorted(maps.Keys(jumps)) {
		seq := normalizeSequence(strings.TrimSpace(binding))
		target, ok := domain.ParseJump(jumps[binding])
		switch {
		case !sequenceValid(seq):
			k.issues = append(k.issues,
				fmt.Sprintf("jump.%s: %q is not a key name the terminal reports; ignored", binding, binding))
		case !ok:
			k.issues = append(k.issues, fmt.Sprintf(
				"jump.%s: %q is not a place — write one of %s followed by a name",
				binding, jumps[binding], strings.Join(domain.JumpKinds(), ", ")))
		case k.jumpConflict(seq) != "":
			k.issues = append(k.issues,
				fmt.Sprintf("jump.%s: %s — unbind it with \"-\" first", binding, k.jumpConflict(seq)))
		default:
			k.jumps[seq] = target
			k.claimPrefixes(scopeGlobal, seq)
		}
	}
}

// bindScripts records the sequences bound to user commands, under bindJumps' rules;
// two commands on one sequence is refused rather than resolved by file order.
func (k *keymap) bindScripts(scripts []config.Script) {
	for _, script := range scripts {
		name := strings.TrimPrefix(strings.TrimSpace(script.Name), "/")
		for _, seq := range splitKeys(script.Keys) {
			switch {
			case !sequenceValid(seq):
				k.issues = append(k.issues, fmt.Sprintf(
					"commands.script %q: %q is not a key name the terminal reports; ignored", name, seq))
			case k.scripts[seq] != "":
				k.issues = append(k.issues, fmt.Sprintf(
					"commands.script %q: %q already runs /%s", name, spellSequence(seq), k.scripts[seq]))
			case k.jumpConflict(seq) != "":
				k.issues = append(k.issues, fmt.Sprintf(
					"commands.script %q: %s — unbind it with \"-\" first", name, k.jumpConflict(seq)))
			default:
				if k.scripts == nil {
					k.scripts = map[string]string{}
				}
				k.scripts[seq] = name
				k.claimPrefixes(scopeGlobal, seq)
			}
		}
	}
}

// scriptFor is the command a sequence runs, if one does.
func (k keymap) scriptFor(seq string) (string, bool) {
	name, ok := k.scripts[seq]
	return name, ok
}

// prefixShadow says why a multi-step binding can never fire (a shorter prefix is
// already a complete binding in a scope it resolves beside), or "".
func (k keymap) prefixShadow(owner scope, seq string) string {
	steps := strings.Split(seq, " ")
	if len(steps) < 2 {
		return ""
	}
	// Only scopes resolved beside this one can shadow it: `s` in the timeline must
	// not block `s u` in the room list.
	against := append([]scope{owner}, sharedScopes...)
	for i := 1; i < len(steps); i++ {
		prefix := strings.Join(steps[:i], " ")
		for _, s := range against {
			if act, ok := k.binds[s][prefix]; ok {
				return fmt.Sprintf("%q can never fire: %q already means %s on its own",
					spellSequence(seq), spellSequence(prefix), actionName(s, act))
			}
		}
	}
	return ""
}

// claimPrefixes records a sequence's incomplete forms against the owning scope.
func (k *keymap) claimPrefixes(s scope, seq string) {
	steps := strings.Split(seq, " ")
	for i := 1; i < len(steps); i++ {
		prefix := strings.Join(steps[:i], " ")
		if k.prefixes[prefix] == nil {
			k.prefixes[prefix] = map[scope]bool{}
		}
		k.prefixes[prefix][s] = true
	}
}

// sharedScopes are consulted alongside whichever pane has focus.
var sharedScopes = []scope{scopeGlobal, scopeNav, scopeCommand, scopePlayer}

// jumpConflict says why seq cannot be bound to a place, or "" when it can: it is
// an action's key, the start of a longer binding, or extends a complete one.
// Rebinding another place is not a conflict.
func (k keymap) jumpConflict(seq string) string {
	if s, act, ok := k.actionFor(seq); ok {
		return "already bound to " + actionName(s, act)
	}
	if k.incompleteAnywhere(seq) {
		return "the start of a longer binding — that one would become unreachable"
	}
	steps := strings.Split(seq, " ")
	for i := 1; i < len(steps); i++ {
		prefix := strings.Join(steps[:i], " ")
		if s, act, ok := k.actionFor(prefix); ok {
			return fmt.Sprintf("%q already means %s on its own, so the rest of the sequence would never arrive",
				spellSequence(prefix), actionName(s, act))
		}
	}
	return ""
}

// actionFor is the action a key or sequence is bound to, and the scope holding it.
// actionFor is the action a sequence is bound to and its scope, walking scopes in
// help order so a collision is always reported against the same binding.
func (k keymap) actionFor(seq string) (scope, action, bool) {
	for _, section := range scopeTitles {
		if act, ok := k.binds[section.scope][seq]; ok {
			return section.scope, act, true
		}
	}
	return scopeGlobal, actNone, false
}

// jumpFor is the place a completed sequence goes to, if it is one.
func (k keymap) jumpFor(seq string) (domain.JumpTarget, bool) {
	target, ok := k.jumps[seq]
	return target, ok
}

// clash explains why key could not be bound to want, naming what holds it.
func (k keymap) clash(s scope, want, key string) string {
	holder := actionName(s, k.binds[s][key])
	return fmt.Sprintf("%s: %q is already bound to %s in this mode", want, key, holder)
}

// bindAll binds every key it can to act, returning how many landed and which clashed.
func (k *keymap) bindAll(s scope, act action, keys []string) (bound int, clashes []string) {
	for _, key := range keys {
		if held, taken := k.binds[s][key]; taken && held != act {
			clashes = append(clashes, key)
			continue
		}
		k.bind(s, key, act)
		bound++
	}
	return bound, clashes
}

// claim records key → act (and its prefixes) without labeling it for help.
func (k *keymap) claim(s scope, key string, act action) {
	if k.binds[s] == nil {
		k.binds[s] = map[string]action{}
		k.labels[s] = map[action][]string{}
	}
	k.binds[s][key] = act
	k.claimPrefixes(s, key)
}

// incomplete reports whether seq could still grow into a binding in one of scopes.
// A complete binding is never incomplete: if "g" and "g g" are both bound, "g" fires.
func (k keymap) incomplete(seq string, scopes ...scope) bool {
	owners := k.prefixes[seq]
	for _, s := range scopes {
		if owners[s] {
			return true
		}
	}
	return false
}

// incompleteAnywhere is incomplete over every scope, for config checks.
func (k keymap) incompleteAnywhere(seq string) bool { return len(k.prefixes[seq]) > 0 }

// boundIn reports whether seq is a complete binding in one of scopes, or a jump or
// script (bound wherever keys are commands). Asking about any scope instead broke
// `y y` in the timeline once `y` was bound in the room list.
func (k keymap) boundIn(seq string, scopes ...scope) bool {
	// Scripts must be checked too, or multi-step script bindings are abandoned
	// mid-sequence.
	if _, ok := k.jumps[seq]; ok {
		return true
	}
	if _, ok := k.scripts[seq]; ok {
		return true
	}
	for _, s := range scopes {
		if _, ok := k.binds[s][seq]; ok {
			return true
		}
	}
	return false
}

// bind records key → act and lists the key under act in the help overlay.
func (k *keymap) bind(s scope, key string, act action) {
	k.claim(s, key, act)
	if !slices.Contains(k.labels[s][act], key) {
		k.labels[s][act] = append(k.labels[s][act], key)
	}
}

// actionName is an action's config path within a scope, for issue messages.
func actionName(s scope, act action) string {
	if i := slices.IndexFunc(keyActions, func(r keyAction) bool { return r.scope == s && r.act == act }); i >= 0 {
		return keyActions[i].name
	}
	return "an unnamed action"
}

// lookup resolves key to an action, trying scopes in order; actNone when unbound.
func (k keymap) lookup(key string, scopes ...scope) action {
	for _, s := range scopes {
		if act, ok := k.binds[s][key]; ok {
			return act
		}
	}
	return actNone
}

// namedKeys are the non-character key names Bubble Tea reports (from
// tea.KeyPressMsg.String()); f1–f20 are matched by pattern.
var namedKeys = map[string]bool{
	"enter": true, "esc": true, "tab": true, "space": true, "backspace": true,
	"delete": true, "insert": true, "up": true, "down": true, "left": true,
	"right": true, "home": true, "end": true, "pgup": true, "pgdown": true,
	"begin": true, "menu": true, "find": true, "select": true, "pause": true,
	"capslock": true, "numlock": true, "scrolllock": true, "printscreen": true,
	"plus": true, "minus": true, "comma": true, "period": true,
}

// modifiers are the prefixes a key name may carry, in any combination.
var modifiers = []string{"ctrl+", "alt+", "shift+", "meta+", "super+", "hyper+"}

// validKey reports whether name is modifiers over a single character or a named key.
func validKey(name string) bool {
	base := name
	for stripped := true; stripped; {
		stripped = false
		for _, mod := range modifiers {
			if rest, cut := strings.CutPrefix(base, mod); cut && rest != "" {
				base, stripped = rest, true
				break
			}
		}
	}
	if len([]rune(base)) == 1 {
		return true
	}
	if namedKeys[base] {
		return true
	}
	if digits, isFn := strings.CutPrefix(base, "f"); isFn {
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 && n <= 20 {
			return true
		}
	}
	return false
}

// splitKeys parses a comma-separated binding list, dropping blanks. The name "minus"
// is the - key ("-" alone binds nothing); the name "comma"
// stands for "," since the separator cannot be written otherwise.
func splitKeys(list string) []string {
	parts := strings.Split(list, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		switch key := strings.TrimSpace(p); key {
		case "":
		case "comma":
			keys = append(keys, ",")
		case "minus":
			keys = append(keys, "-")
		default:
			keys = append(keys, normalizeSequence(key))
		}
	}
	return keys
}

// shorthandSteps is the longest run-together sequence accepted without spaces ("gg").
const shorthandSteps = 3

// normalizeSequence canonicalizes a binding to steps joined by one space. "gg" is
// accepted as "g g", but only for 2–3 letters/digits that are not a key name, so a
// typo like "escape" stays intact for validation to report.
func normalizeSequence(binding string) string {
	if fields := strings.Fields(binding); len(fields) > 1 {
		return strings.Join(fields, " ")
	}
	if validKey(binding) {
		return binding
	}
	runes := []rune(binding)
	if len(runes) < 2 || len(runes) > shorthandSteps {
		return binding
	}
	steps := make([]string, len(runes))
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return binding
		}
		steps[i] = string(r)
	}
	return strings.Join(steps, " ")
}

// sequenceValid reports whether every step of a binding is a valid key.
func sequenceValid(binding string) bool {
	for step := range strings.SplitSeq(binding, " ") {
		if !validKey(step) {
			return false
		}
	}
	return true
}

// keysFor lists the keys bound to act in declaration order, spelled for humans.
func (k keymap) keysFor(s scope, act action) []string {
	keys := k.labels[s][act]
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = spellSequence(key)
	}
	return out
}

// spellSequence renders "g g" as "gg"; a named or modified step keeps its spaces.
func spellSequence(key string) string {
	steps := strings.Split(key, " ")
	if len(steps) < 2 {
		return key
	}
	for _, step := range steps {
		if len([]rune(step)) != 1 {
			return key
		}
	}
	return strings.Join(steps, "")
}

// commandKeyHint is keyHint restricted to keys that produce no text, for modes that
// filter as you type.
func (k keymap) commandKeyHint(s scope, act action) string {
	for _, key := range k.keysFor(s, act) {
		if !typesText(key) {
			return key
		}
	}
	return ""
}

// typesText reports whether pressing key would produce a character.
func typesText(key string) bool {
	if strings.Contains(key, "+") {
		return false
	}
	return len([]rune(key)) == 1
}

// keyHint is the first key bound to act (the config's preference), or "".
func (k keymap) keyHint(s scope, act action) string {
	if keys := k.keysFor(s, act); len(keys) > 0 {
		return keys[0]
	}
	return ""
}
