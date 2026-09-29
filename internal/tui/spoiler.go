package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// toggleReveal uncovers or re-covers what the message under the cursor hides: a
// spoiler, or the kept words of a deleted message. Per message, not remembered.
func (m Model) toggleReveal() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	if !m.hasSomethingHidden(msg) {
		// A deleted row with nothing kept gets an answer; silence there reads as a
		// broken key.
		if msg.Redacted {
			return m.say("nothing was kept for that one — [display.deleted] keep was off when it was deleted"), nil
		}
		return m, nil
	}
	m.timeline.revealed = withEntry(m.timeline.revealed, msg.ID, !m.timeline.revealed[msg.ID])
	return m, nil
}

// hasSomethingHidden reports whether a message is covering anything up.
func (m Model) hasSomethingHidden(msg domain.Message) bool {
	if msg.Redacted {
		return msg.Body != "" // only when the words were kept
	}
	_, spans, ok := m.formattedBody(msg)
	if !ok {
		return false
	}
	for i := range spans {
		if spans[i].Spoiler {
			return true
		}
	}
	return false
}
