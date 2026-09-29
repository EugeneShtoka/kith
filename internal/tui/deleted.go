package tui

import (
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// withDeletionsHidden drops the deletions [display.deleted] asks to hide, yours and
// others' separately. It filters `shown`, so every index into the pane agrees. A
// thread anchor is always kept, or its thread would have no row to be reached from.
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

// fromMe reports whether a sender is you: this account, or one grouped with it by
// [[display.identity]] (e.g. a bridged puppet of your phone).
func (m Model) fromMe(sender string) bool {
	if sender == "" {
		return false
	}
	if sender == m.me {
		return true
	}
	me, ok := m.prefs.identities[m.me]
	if !ok || me.alias == "" {
		return false
	}
	them, ok := m.prefs.identities[sender]
	return ok && them.alias == me.alias
}

// deletedBody is the placeholder a deleted message draws: "(deleted)", or who removed
// it (and why) when that was somebody other than the sender.
func (m Model) deletedBody(msg domain.Message) string {
	if msg.RedactedBy == "" || msg.RedactedBy == msg.Sender {
		return redactedBody
	}
	// Name and reason isolated so an RTL reason cannot reorder the sentence.
	who := isolate(m.processedMentionName(msg.RedactedBy, localpart(msg.RedactedBy), msg.RoomID))
	if msg.RedactedReason == "" {
		return "(deleted by " + who + ")"
	}
	return "(deleted by " + who + " — " + isolate(oneLine.Replace(msg.RedactedReason)) + ")"
}

// localpart is the readable half of an MXID.
func localpart(mxid string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(mxid, "@"), ":")
	return name
}
