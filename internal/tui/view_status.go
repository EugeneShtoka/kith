package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) renderStatus() string {
	// A question or prompt owns the whole row.
	if m.confirm.active() {
		return m.theme.Muted.Render(drawLine(m.hintLine(
			note(m.confirmPrompt()),
			keyed(m.keys.keyHint(scopeConfirm, actYes), "yes"),
			keyed(m.keys.keyHint(scopeConfirm, actNo), "no"),
		), lineSpec{width: m.width, sentence: true}))
	}
	if m.editingSettingRow() {
		// The value is typed on its row; here, what it may be and the keys.
		return m.theme.Muted.Render(drawLine(m.hintLine(
			note(m.settingHelp()),
			keyed(m.keys.keyHint(scopePrompt, actSubmit), "save"),
			keyed(m.keys.keyHint(scopePrompt, actCancel), "keep it as it was"),
		), lineSpec{width: m.width, sentence: true}))
	}
	if m.prompt.active() {
		label, hint := m.prompt.label(), m.promptHint()
		if m.prompt.kind == promptJumpBind {
			label = m.bindingLabel()
		}
		const gap = "   —   "
		room := m.width - ansi.StringWidth(label) - ansi.StringWidth(hint) - len(gap)
		return m.theme.Muted.Render(clamp(
			label+editedLine(m.editorFor(fieldPrompt), room, m.typingField() == fieldPrompt)+gap+hint,
			m.width))
	}

	left := m.status()
	// A pending key sequence owns the slot.
	if pending := m.chordPending(); pending != "" {
		return m.theme.Muted.Render(drawLine(pending+"   —   "+m.hints(), lineSpec{width: m.width, sentence: true}))
	}
	// The standing line is the cursor position, unless an event is news.
	if !m.sayingSomething() {
		if pos := m.timelinePosition(); pos != "" {
			left = pos
		}
	}
	if left == "" {
		left = "ready"
	}
	badge := m.connectionBadge()
	if badge == "" {
		badge = m.silenceBadge()
	}
	room := m.width - ansi.StringWidth(badge)
	// Drawn apart from the legend so an RTL name cannot pull in its leading digits.
	drawn := drawLine(left, lineSpec{width: room, sentence: true})
	return m.theme.Muted.Render(clamp(drawn+"   —   "+m.hints(), room)) + badge
}

// timelinePosition is where the message cursor sits among the loaded messages —
// "230/421", and nothing else. It is empty outside the timeline and while an overlay
// owns the pane, since it would then be describing something not on screen.
func (m Model) timelinePosition() string {
	if m.focus != paneTimeline || len(m.timeline.messages) == 0 || m.picker.active() || m.search.active {
		return ""
	}
	return fmt.Sprintf("%d/%d", m.selectedIndex()+1, len(m.timeline.messages))
}

// hints is the context-sensitive key legend, read from the keymap.
func (m Model) hints() string {
	// A chooser holds the keyboard wherever it was opened.
	if m.picker.active() {
		return m.pickerHint()
	}
	if m.focus == paneTimeline {
		return m.timelineHints()
	}
	nav := keyed(m.keys.keyHint(scopeNav, actUp)+"/"+m.keys.keyHint(scopeNav, actDown), "navigate")
	if m.focus == paneRooms {
		if room, ok := m.currentRoom(); ok && room.IsInvite() {
			return m.hintLine(
				keyed(m.keys.keyHint(scopeRooms, actAccept), "accept"),
				keyed(m.keys.keyHint(scopeRooms, actReject), "reject"),
				nav,
				keyed(m.keys.keyHint(scopeCommand, actHelp), "help"),
			)
		}
		// A thread row: join/leave would act on the room above, so offer thread actions.
		if row, ok := m.selectedRow(); ok && row.isThread() {
			return m.hintLine(
				nav,
				keyed(m.keys.keyHint(scopeNav, actOpen), "open thread"),
				keyed(m.keys.keyHint(scopeRooms, actMarkRead), "mark thread read"),
				keyed(m.keys.keyHint(scopeCommand, actHelp), "help"),
			)
		}
		return m.hintLine(
			nav,
			keyed(m.keys.keyHint(scopeNav, actOpen), "open"),
			keyed(m.keys.keyHint(scopeRooms, actListThreads), "threads"),
			keyed(m.keys.keyHint(scopeRooms, actMarkRead), "mark read"),
			keyed(m.keys.keyHint(scopeRooms, actJoin), "join"),
			keyed(m.keys.keyHint(scopeRooms, actLeave), "leave"),
			keyed(m.keys.keyHint(scopeCommand, actHelp), "help"),
		)
	}
	return m.hintLine(
		keyed(m.keys.keyHint(scopeGlobal, actFocusNext), "pane"),
		nav,
		keyed(m.keys.keyHint(scopeNav, actOpen), "open"),
		keyed(m.keys.keyHint(scopeRail, actMarkRead), "mark read"),
		keyed(m.keys.keyHint(scopeGlobal, actJumpTo), "go to"),
		keyed(m.keys.keyHint(scopeCommand, actSettings), "settings"),
		keyed(m.keys.keyHint(scopeCommand, actHelp), "help"),
		keyed(m.keys.keyHint(scopeCommand, actQuit), "quit"),
	)
}

// timelineHints is the timeline pane's legend, which changes with its mode: the
// pane is a results list, an emoji grid, a react prompt, a composer or a message
// browser depending on what you are doing in it.
func (m Model) timelineHints() string {
	switch {
	case m.search.active:
		return m.searchHint()
	case m.picker.active():
		return m.pickerHint()
	case m.compose.reacting:
		return m.hintLine(
			keyed("1-9", "quick react"),
			note("or type :name:/emoji"),
			keyed(m.keys.keyHint(scopeReact, actSend), "send"),
			keyed(m.keys.keyHint(scopeReact, actCancel), "cancel"),
		)
	case m.compose.insertMode:
		// Both send and newline are configurable, so both are named.
		return m.hintLine(
			keyed(m.keys.keyHint(scopeInsert, actSend), "send"),
			keyed(m.keys.keyHint(scopeInsert, actNewline), "new line"),
			keyed(m.keys.keyHint(scopeInsert, actCancel), "normal mode"),
			note("@ mention"),
			note(":emoji:"),
			keyed(m.keys.keyHint(scopeTimeline, actOpenEmoji), "react from the grid"),
		)
	default:
		// Keys that apply only to some messages appear only when they do.
		return m.hintLine(
			keyed(m.keys.keyHint(scopeNav, actDown)+"/"+m.keys.keyHint(scopeNav, actUp), "select"),
			keyed(m.keys.keyHint(scopeTimeline, actNextMention)+"/"+m.keys.keyHint(scopeTimeline, actPrevMention), "mention"),
			keyedIf(m.hasAttachments(),
				m.keys.keyHint(scopeTimeline, actNextAttachment)+"/"+m.keys.keyHint(scopeTimeline, actPrevAttachment), "file"),
			keyed(m.keys.keyHint(scopeTimeline, actReply), "reply"),
			keyed(m.keys.keyHint(scopeTimeline, actReact), "react"),
			keyedIf(m.hasThread() && !m.thread.open(), m.keys.keyHint(scopeTimeline, actOpenThread), "open thread"),
			keyedIf(m.thread.open(), m.keys.keyHint(scopeTimeline, actName), "name thread"),
			keyedIf(m.hasCode(), m.keys.keyHint(scopeTimeline, actCopyCode), "copy code"),
			keyedIf(m.hasVoiceNote(), m.keys.keyHint(scopeTimeline, actPlay), "play"),
			keyedIf(m.hasVideo(), m.keys.keyHint(scopeTimeline, actPlay), "watch"),
			keyedIf(m.hasAttachment(), m.keys.keyHint(scopeTimeline, actDownload), "save file"),
			keyedIf(m.hasAttachment(), m.keys.keyHint(scopeTimeline, actSaveAs), "save as"),
			keyed(m.keys.keyHint(scopeTimeline, actInsert), "compose"),
			keyed(m.keys.keyHint(scopeNav, actSelectOldest)+"/"+m.keys.keyHint(scopeNav, actSelectNewest), "ends"),
			keyed(m.keys.keyHint(scopeNav, actBack), m.backLabel()),
			keyed(m.keys.keyHint(scopeCommand, actHelp), "help"),
		)
	}
}

// backLabel is what the back key does here: it closes an open thread.
func (m Model) backLabel() string {
	if m.thread.open() {
		return "close thread"
	}
	return "back"
}

// promptHint is the open prompt's key legend. A search prompt already shows its
// result count in the pane, so it advertises the keys that leave the prompt rather
// than one to "run" a search that is running as you type.
func (m Model) promptHint() string {
	submit := "submit"
	switch m.prompt.kind {
	case promptJoin:
		submit = "join"
	case promptSearchRoom, promptSearchAll, promptMentions, promptFiles, promptStarred,
		promptTracked, promptCaught:
		submit = "walk the results"
	case promptAlias, promptRoomName, promptGroupName, promptThreadName, promptRuleSound,
		promptRuleName, promptAttach, promptSetting, promptSettingEntry, promptInvite, promptUnban,
		promptNewRoom, promptJumpBind, promptCommand, promptTagName, promptTagEntry, promptNone:
		// "submit" says it for these.
	}
	hints := []hint{
		keyed(m.keys.keyHint(scopePrompt, actSubmit), submit),
		keyed(m.keys.keyHint(scopePrompt, actCancel), "cancel"),
	}
	if next, ok := m.nextBindingTarget(); ok && m.prompt.kind == promptJumpBind {
		hints = append(hints, keyed(m.keys.keyHint(scopePrompt, actSearchScope), m.targetName(next)))
	}
	return m.hintLine(hints...)
}

// hint is one entry in the status legend: the key(s) to press and what they do.
// bare marks an entry that never had a key of its own — see note.
type hint struct {
	keys, what string
	bare       bool
}

// keyed is a legend entry for an action: the keys bound to it, and what it does.
func keyed(keys, what string) hint { return hint{keys: keys, what: what} }

// keyedIf is a legend entry shown only when it applies.
func keyedIf(when bool, keys, what string) hint {
	if !when {
		return hint{}
	}
	return keyed(keys, what)
}

// note is a legend entry with no key, like "type to compose". It is distinct from a
// keyed entry whose key came back unbound: that one is dropped, this one is shown.
func note(what string) hint { return hint{what: what, bare: true} }

// hintLine joins hints as "key: what · key: what", dropping any whose key came back
// unbound — the legend never advertises a key that isn't bound, nor names an action
// with no way to reach it. A "/" left dangling by the unbound half of a pair is
// trimmed off too.
func (m Model) hintLine(hints ...hint) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		keys := strings.Trim(h.keys, "/")
		switch {
		case h.bare:
			parts = append(parts, h.what)
		case keys != "":
			parts = append(parts, keys+": "+h.what)
		}
	}
	return strings.Join(parts, " · ")
}
