package tui

import (
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// inventory is the joined rooms, invitations, their union, and the space hierarchy. all is
// rebuilt only through the setters, so it cannot go stale.
type inventory struct {
	joined  []domain.Room
	invites []domain.Room
	all     []domain.Room
	spaces  []domain.Space
	// holders is built from spaces by withSpaces; see spaceNames.
	holders *spaceHolders
}

func (inv inventory) withJoined(joined []domain.Room) inventory {
	inv.joined = joined
	return inv.recombined()
}

// withInvites replaces the invitation set (authoritative).
func (inv inventory) withInvites(invites []domain.Room) inventory {
	inv.invites = invites
	return inv.recombined()
}

func (inv inventory) withSpaces(spaces []domain.Space) inventory {
	inv.spaces = spaces
	inv.holders = indexHolders(spaces)
	return inv
}

// spaceHolders maps each room to its holding spaces' names, in hierarchy order. Every draw
// asks this per listed room; scanning children per room was too slow at a few hundred rooms.
type spaceHolders struct {
	from  []domain.Space // the slice it was built from
	names map[domain.RoomID][]string
}

func indexHolders(spaces []domain.Space) *spaceHolders {
	h := &spaceHolders{from: spaces, names: map[domain.RoomID][]string{}}
	for i := range spaces {
		name := spaces[i].DisplayName()
		// A child listed twice is held once, as a scan would find it.
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

// spaceNames is the names of the spaces holding id, in hierarchy order. The index is used when
// built from the current spaces; otherwise (a test assigning the field) the spaces are scanned.
func (inv inventory) spaceNames(id domain.RoomID) []string {
	if h := inv.holders; h != nil && sameSpaces(h.from, inv.spaces) {
		return slices.Clip(h.names[id]) // clipped so a caller's append cannot write into the index
	}
	var names []string
	for i := range inv.spaces {
		if slices.Contains(inv.spaces[i].Children, id) {
			names = append(names, inv.spaces[i].DisplayName())
		}
	}
	return names
}

// sameSpaces reports whether a and b are the same slice, not just equal.
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

func (inv inventory) byID(id domain.RoomID) (domain.Room, bool) {
	for i := range inv.all {
		if inv.all[i].ID == id {
			return inv.all[i], true
		}
	}
	return domain.Room{}, false
}

// removeRoom returns rooms without id; the three-index slice keeps append off the caller's
// array.
func removeRoom(rooms []domain.Room, id domain.RoomID) []domain.Room {
	at := indexOfRoom(rooms, id)
	if at < 0 {
		return rooms
	}
	return append(rooms[:at:at], rooms[at+1:]...)
}
