package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A room's topic (m.room.topic): shown after the title when it fits, in full with
// `gt`. Read-only by design.

// topicMinWidth is the least space a truncated topic needs to be worth showing.
const topicMinWidth = 24

// topicSuffix is the topic with its separator as shown after the title, or empty when
// it doesn't fit.
func (m Model) topicSuffix(title string, width int) string {
	room, ok := m.currentRoom()
	if !ok || room.Topic == "" || m.thread.open() {
		return ""
	}
	topic := flatten(room.Topic)
	if topic == "" {
		return ""
	}
	const sep = "  ·  "
	left := width - ansi.StringWidth(title) - ansi.StringWidth(sep)
	if left < topicMinWidth {
		return ""
	}
	return sep + isolate(truncateLogical(topic, left))
}

func (m Model) topicText() string {
	room, ok := m.currentRoom()
	if !ok {
		return ""
	}
	return strings.TrimSpace(room.Topic)
}

func (m Model) openTopic() (Model, tea.Cmd) {
	if m.topicText() == "" {
		return m.say("this room has no topic"), nil
	}
	m.reader = m.reader.opening(readerTopic)
	return m, nil
}

func (m Model) topicTitle() string {
	room, ok := m.currentRoom()
	if !ok {
		return "Topic"
	}
	return m.roomName(room)
}

// topicLines is the topic with its own line breaks kept.
func (m Model) topicLines() []string {
	return strings.Split(m.topicText(), "\n")
}
