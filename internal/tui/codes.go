package tui

import (
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// codeSettings is what counts as a verification code (rules) and where kith looks
// for one unprompted (scope).
type codeSettings struct {
	rules domain.CodeRules
	scope domain.CodeScope
}

// codesHere reports whether kith looks for verification codes in roomID
// unprompted. Pressing `c` is explicit and is not gated by the scope.
func (m Model) codesHere(roomID domain.RoomID) bool {
	room, ok := m.roomByID(roomID)
	if !ok {
		return false
	}
	return m.prefs.codes.scope.Admits(m.factsFor(room))
}

// factsFor is what a list entry can match about one room. The protocol comes from
// the bridge space owning the room — the same rule the daemon uses — not from
// roomProtocol(), which guesses from the open room's senders.
func (m Model) factsFor(room domain.Room) domain.RoomFacts {
	facts := domain.RoomFacts{
		ID:       string(room.ID),
		Name:     m.roomLabel(room),
		Spaces:   m.spacesOf(room.ID),
		Direct:   room.IsDirect,
		Protocol: domain.ProtocolMatrix,
	}
	// Computed from pin entries, which cannot themselves say `pinned`.
	facts.Pinned = m.rail.pinned.Pins(facts)
	for i := range m.rooms.spaces {
		if !m.rooms.spaces[i].Bridge.IsBridged() {
			continue
		}
		if slices.Contains(m.rooms.spaces[i].Children, room.ID) {
			facts.Protocol = m.rooms.spaces[i].Bridge
			return facts
		}
	}
	return facts
}
