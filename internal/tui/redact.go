package tui

import (
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Deleting a message: a key on the message cursor that asks first, folds the row at
// once without waiting for the echo, and restores it on failure. Another's message
// needs the room's redact level, checked by the backend.

// askRedact asks whether to delete the message under the cursor.
func (m Model) askRedact() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	if msg.Redacted {
		return m.say("that message is already deleted"), nil
	}
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	m.confirm = confirmState{action: pendingRedact, roomID: room.ID, event: msg.ID, mine: msg.Sender == m.me}
	return m, nil
}

// redactPrompt names whose message it is.
func (m Model) redactPrompt(pending confirmState) string {
	whose := "somebody else's"
	if pending.mine {
		whose = "your"
	}
	return "delete " + whose + " message? everyone sees it go"
}

// redact carries out a confirmed deletion, folding the row immediately.
func (m Model) redact(pending confirmState) (Model, tea.Cmd) {
	// The same synthetic redaction an incoming one arrives as, so one fold path.
	if pending.roomID == m.openRoom {
		m = m.setMessages(domain.MergeMessages(m.timeline.messages, []domain.Message{{
			ID: pending.event, RoomID: pending.roomID, Redacted: true,
		}}))
	}
	m = m.doing("deleting…")
	return m, m.redactCmd(pending.roomID, pending.event)
}

// handleRedacted reports a failed deletion and restores the row; success is silent.
func (m Model) handleRedacted(msg redactedMsg) (Model, tea.Cmd) {
	if msg.err == nil {
		return m.clearStatus(), nil
	}
	// The message is gone, only its edit history remains: do not restore it.
	if errors.Is(msg.err, api.ErrEditsRemain) {
		m.logErr(slog.LevelWarn, "delete left its edit history", msg.err, "room", msg.roomID)
		return m.say("deleted — but " + msg.err.Error()), nil
	}
	m = m.sayErr("could not delete it", msg.err)
	if msg.roomID == m.openRoom {
		return m, m.cachedTimelineCmd(msg.roomID)
	}
	return m, nil
}
