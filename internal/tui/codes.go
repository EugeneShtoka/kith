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

// factsFor is what a list entry can match about one room, as the daemon and every
// other scope read it (domain.Places.Facts): the name you gave it or its own, never
// the shortened room-list label; the network from the bridge space owning it, not
// from roomProtocol(), which guesses from the open room's senders.
func (m Model) factsFor(room domain.Room) domain.RoomFacts {
	places := domain.Places{Names: m.prefs.roomAliases, Order: m.rail.homes, Tags: m.rail.tags, Archives: m.rail.archives}
	return places.Facts(room, domain.HoldersOf(room.ID, m.rooms.spaces))
}
