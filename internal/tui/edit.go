package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// askEdit starts editing the message under the cursor, in the composer. The
// half-written draft is put aside and restored if the edit is abandoned.
func (m Model) askEdit() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	switch {
	case !m.isMe(msg.Sender):
		return m.say("you can only edit your own messages"), nil
	case msg.Redacted:
		return m.say("that message is deleted"), nil
	case msg.Media != nil:
		return m.say("an attachment cannot be edited"), nil
	case msg.Body == "":
		return m.say("there is nothing there to edit"), nil
	}
	m.compose.editSaved = m.editorFor(fieldComposer).text
	m.compose.editing = msg.ID
	m = m.store(fieldComposer, newEditor(msg.Body).end())
	// The message's pills come along, so a revision keeps mentioning whom it did.
	m.compose.insertMode, m.compose.replyTo, m.compose.drafted = true, "", slices.Clone(msg.Mentions)
	return m, nil
}

// cancelEdit abandons an edit and restores the composer's previous text. Called from
// the one place leaving insert mode is handled.
func (m Model) cancelEdit() Model {
	if !m.compose.isEditing() {
		return m
	}
	m.compose.editing = ""
	m = m.store(fieldComposer, newEditor(m.compose.editSaved).end())
	m.compose.editSaved = ""
	return m.say("edit canceled")
}

// submitEdit sends the revision and folds it into the timeline at once.
func (m Model) submitEdit(room domain.Room, body string) (Model, tea.Cmd) {
	target, mentions := m.compose.editing, m.compose.drafted
	if body == "" {
		// Refused rather than treated as a deletion, which has its own key and question.
		return m.say("an empty edit would delete it — " +
			m.keys.keyHint(scopeTimeline, actRedact) + " on the message does that, and asks first"), nil
	}
	m.compose = m.compose.cleared()
	// Folded optimistically, in the same shape an incoming edit arrives as.
	if room.ID == m.openRoom {
		m = m.setMessages(domain.MergeMessages(m.timeline.messages, []domain.Message{{
			ID: target, RoomID: room.ID, Body: body, Edited: true,
		}}))
	}
	revision := domain.Draft{
		Body: body, Mentions: mentions, Edits: target,
		// Rendered as a new message would be: an edit is no way around markdown = false.
		Plain: !m.conf.base.Composer.MarkdownEnabled(),
	}
	m = m.doing("editing…")
	// The revision ends the typing notice, as a send does.
	m, stop := m.stopTyping()
	return m, tea.Batch(m.editCmd(room.ID, revision), stop)
}

// handleEdited reports a failed edit and reloads the timeline to undo the
// optimistic fold.
func (m Model) handleEdited(msg editedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m.clearStatus(), nil
	}
	m = m.sayErr("could not edit it", msg.err)
	if msg.roomID == m.openRoom {
		return m, m.cachedTimelineCmd(msg.roomID)
	}
	return m, nil
}
