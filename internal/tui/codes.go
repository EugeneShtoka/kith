package tui

import (
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

// factsFor is what a list entry can match about a room, as every scope reads it
// (domain.Places.Facts): its given or own name, never the shortened list label; the
// network from its owning bridge space, not roomProtocol(), which guesses from senders.
func (m Model) factsFor(room domain.Room) domain.RoomFacts {
	places := domain.Places{Names: m.prefs.roomAliases, Order: m.rail.homes, Tags: m.rail.tags, Archives: m.rail.archives}
	return places.Facts(room, domain.HoldersOf(room.ID, m.rooms.spaces))
}
