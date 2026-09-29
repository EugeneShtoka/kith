package tui

import (
	"github.com/EugeneShtoka/kith/internal/domain"
)

// trackedIn is the tracked-word list in force for this message's room and sender
// (rules combine by union, see domain.TrackedFor). Asked per message per frame, so
// the empty case is one length check.
func (m Model) trackedIn(msg domain.Message) domain.Tracked {
	rules := m.prefs.tracked
	if len(rules) == 0 {
		return domain.Tracked{}
	}
	room, ok := m.roomByID(msg.RoomID)
	if !ok {
		// Unknown room: only rules naming nowhere can still be judged.
		return domain.TrackedFor(rules, domain.RoomFacts{ID: string(msg.RoomID)}, msg.Sender)
	}
	return domain.TrackedFor(rules, m.factsFor(room), msg.Sender)
}
