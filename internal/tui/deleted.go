package tui

import (
	"github.com/EugeneShtoka/kith/internal/domain"
)

// withDeletionsHidden drops the deletions [display.deleted] hides, yours and others'
// separately. It filters `shown`, so all pane indices agree. Thread anchors are kept,
// or their threads would have no row.
func (m Model) withDeletionsHidden(msgs []domain.Message, threads []domain.Thread) []domain.Message {
	mine, others := m.prefs.display.Deleted.HideMine(), m.prefs.display.Deleted.HideOthers()
	if !mine && !others {
		return msgs
	}
	anchors := make(map[domain.EventID]bool, len(threads))
	for i := range threads {
		anchors[threads[i].Anchor] = true
	}
	out := make([]domain.Message, 0, len(msgs))
	for i := range msgs {
		if m.hiddenDeletion(msgs[i], mine, others) && !anchors[msgs[i].ID] {
			continue
		}
		out = append(out, msgs[i])
	}
	return out
}

// hiddenDeletion reports whether this message is a deletion the settings hide.
func (m Model) hiddenDeletion(msg domain.Message, mine, others bool) bool {
	if !msg.Redacted {
		return false
	}
	if m.fromMe(msg.Sender) {
		return mine
	}
	return others
}

// fromMe reports whether sender is you: any of your IDs (isMe), or one grouped with
// them by [[display.identity]] (e.g. a bridged puppet of your phone).
func (m Model) fromMe(sender string) bool {
	if sender == "" {
		return false
	}
	if m.isMe(sender) {
		return true
	}
	them, ok := m.prefs.identities[sender]
	if !ok || them.alias == "" {
		return false
	}
	for _, self := range m.selfIDs() {
		if me, ok := m.prefs.identities[self]; ok && me.alias == them.alias {
			return true
		}
	}
	return false
}

// deletedBody is a deleted message's placeholder: "(deleted)", or who removed it (and
// why) when not the sender.
func (m Model) deletedBody(msg domain.Message) string {
	if msg.RedactedBy == "" || msg.RedactedBy == msg.Sender {
		return redactedBody
	}
	// Name and reason isolated so an RTL reason cannot reorder the sentence.
	who := isolate(m.mentionedName(msg.RedactedBy, msg.RoomID, localpart(msg.RedactedBy)))
	if msg.RedactedReason == "" {
		return "(deleted by " + who + ")"
	}
	return "(deleted by " + who + " — " + isolate(oneLine.Replace(msg.RedactedReason)) + ")"
}

// localpart is the readable part of a user ID.
func localpart(userID string) string { return domain.ShortName(userID) }
