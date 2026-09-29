package tui

import (
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// inventory is the joined rooms, invitations, their union, and the space hierarchy.
// joined and invites are each replaced wholesale by their own path; all is only
// rebuilt through the setters, so it cannot go stale.
type inventory struct {
	joined  []domain.Room
	invites []domain.Room
	all     []domain.Room
	spaces  []domain.Space
	// holders is built from spaces by withSpaces; see spaceNames.
	holders *spaceHolders
}

// withJoined replaces the joined-room snapshot.
func (inv inventory) withJoined(joined []domain.Room) inventory {
	inv.joined = joined
	return inv.recombined()
}

// withInvites replaces the invitation set; it is authoritative.
func (inv inventory) withInvites(invites []domain.Room) inventory {
	inv.invites = invites
	return inv.recombined()
}

// withSpaces replaces the space hierarchy.
func (inv inventory) withSpaces(spaces []domain.Space) inventory {
	inv.spaces = spaces
	inv.holders = indexHolders(spaces)
	return inv
}

// spaceHolders maps each room to the names of the spaces holding it, in hierarchy
// order. Every draw asks this of every listed room, and scanning each space's children
// for each room was most of a keypress in a few hundred rooms.
type spaceHolders struct {
	from  []domain.Space // the slice it was built from
	names map[domain.RoomID][]string
}

func indexHolders(spaces []domain.Space) *spaceHolders {
	h := &spaceHolders{from: spaces, names: map[domain.RoomID][]string{}}
	for i := range spaces {
		name := spaces[i].DisplayName()
		// A space listing a child twice still holds it once, as a scan finds it.
		seen := make(map[domain.RoomID]bool, len(spaces[i].Children))
		for _, child := range spaces[i].Children {
			if !seen[child] {
				seen[child] = true
				h.names[child] = append(h.names[child], name)
			}
		}
	}
	return h
}

// spaceNames is the names of the spaces holding id, in hierarchy order. The index
// answers when it was built from the current spaces; a hierarchy set another way (a
// test assigning the field) is scanned.
func (inv inventory) spaceNames(id domain.RoomID) []string {
	if h := inv.holders; h != nil && sameSpaces(h.from, inv.spaces) {
		return slices.Clip(h.names[id]) // clipped: an append by a caller must not write into the index
	}
	var names []string
	for i := range inv.spaces {
		if slices.Contains(inv.spaces[i].Children, id) {
			names = append(names, inv.spaces[i].DisplayName())
		}
	}
	return names
}

// sameSpaces reports whether a and b are the same slice, not merely equal ones.
func sameSpaces(a, b []domain.Space) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// without drops one room from both sources, for a leave whose refresh has not landed.
func (inv inventory) without(id domain.RoomID) inventory {
	inv.invites = removeRoom(inv.invites, id)
	inv.joined = removeRoom(inv.joined, id)
	return inv.recombined()
}

// recombined rebuilds the union, invitations last.
func (inv inventory) recombined() inventory {
	all := make([]domain.Room, 0, len(inv.joined)+len(inv.invites))
	all = append(all, inv.joined...)
	all = append(all, inv.invites...)
	inv.all = all
	return inv
}

// byID is the room with this ID among the union, and whether there is one.
func (inv inventory) byID(id domain.RoomID) (domain.Room, bool) {
	for i := range inv.all {
		if inv.all[i].ID == id {
			return inv.all[i], true
		}
	}
	return domain.Room{}, false
}

// removeRoom is rooms without the one named; the three-index slice keeps append from
// writing into the caller's backing array.
func removeRoom(rooms []domain.Room, id domain.RoomID) []domain.Room {
	at := indexOfRoom(rooms, id)
	if at < 0 {
		return rooms
	}
	return append(rooms[:at:at], rooms[at+1:]...)
}
