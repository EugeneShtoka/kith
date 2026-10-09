package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat its network cannot leave (a Telegram private chat) is deleted instead, by the
// same key and /leave: its history goes, for you, and, if you say so first, for the
// other person too. The last question says which, and nothing is deleted before it.

// askDeleteChat opens the questions for deleting room.
func (m Model) askDeleteChat(room domain.Room) Model {
	if room.Deleting == domain.ChatDeletedForEither {
		m.confirm = confirmState{action: pendingDeleteForThem, room: room}
		return m
	}
	m.confirm = confirmState{action: pendingDeleteChat, room: room}
	return m
}

// answerDeleteForThem takes whether the other person loses the chat too, and asks last
// whether to delete it.
func (m Model) answerDeleteForThem(c confirmState, yes bool) Model {
	m.confirm = confirmState{action: pendingDeleteChat, room: c.room, forEveryone: yes}
	return m
}

// deleteChatPrompt is the question for each step of deleting a chat.
func (m Model) deleteChatPrompt(c confirmState) string {
	name := m.roomName(c.room)
	if c.action == pendingDeleteForThem {
		return "delete the chat with " + name + " for them as well? no deletes it for you only"
	}
	if c.forEveryone {
		return "delete the chat with " + name + " for both of you? its history goes, for them too"
	}
	return "delete the chat with " + name + " for you? its history goes from your account"
}

// deleteChat deletes the chat the questions settled on.
func (m Model) deleteChat(c confirmState) (Model, tea.Cmd) {
	m = m.say("deleting the chat with " + m.roomName(c.room) + "…")
	ctx, backend, roomID, everyone := m.ctx, m.backend, c.room.ID, c.forEveryone
	return m, func() tea.Msg {
		return leftMsg{roomID: roomID, deleted: true, err: backend.DeleteChat(ctx, roomID, everyone)}
	}
}
