package tui

import (
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// inventory is the joined rooms, invitations, chats opened from a link before their network
// lists them, their union, and the space hierarchy. all is rebuilt only through the setters,
// so it cannot go stale.
type inventory struct {
	joined  []domain.Room
	invites []domain.Room
	// fresh are chats a link opened that no listing holds yet (a private chat Telegram makes
	// with its first message); each goes once listed, or once left (keepingFresh).
	fresh  []domain.Room
	all    []domain.Room
	spaces []domain.Space
	// holders is built from spaces by withSpaces; see spaceNames.
	holders spaceHolders
}

func (inv inventory) withJoined(joined []domain.Room) inventory {
	inv.joined = joined
	inv.fresh = slices.DeleteFunc(slices.Clone(inv.fresh), func(r domain.Room) bool {
		return slices.ContainsFunc(joined, func(j domain.Room) bool { return j.ID == r.ID })
	})
	return inv.recombined()
}

// withFresh adds a chat a link opened that no listing holds yet.
func (inv inventory) withFresh(room domain.Room) inventory {
	if _, listed := inv.byID(room.ID); listed {
		return inv
	}
	inv.fresh = append(slices.Clip(inv.fresh), room)
	return inv.recombined()
}

// keepingFresh drops every fresh chat but keep (the open room's): one left before its first
// message was never made.
func (inv inventory) keepingFresh(keep domain.RoomID) inventory {
	inv.fresh = slices.DeleteFunc(slices.Clone(inv.fresh), func(r domain.Room) bool { return r.ID != keep })
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
type spaceHolders map[domain.RoomID][]string

func indexHolders(spaces []domain.Space) spaceHolders {
	h := spaceHolders{}
	for i := range spaces {
		name := spaces[i].DisplayName()
		// A child listed twice is held once, as a scan would find it.
		seen := make(map[domain.RoomID]bool, len(spaces[i].Children))
		for _, child := range spaces[i].Children {
			if !seen[child] {
				seen[child] = true
				h[child] = append(h[child], name)
			}
		}
	}
	return h
}

// spaceNames is the names of the spaces holding id, in hierarchy order.
func (inv inventory) spaceNames(id domain.RoomID) []string {
	return slices.Clip(inv.holders[id]) // clipped so a caller's append cannot write into the index
}

// without drops one room from both sources, for a leave whose refresh has not landed.
func (inv inventory) without(id domain.RoomID) inventory {
	inv.invites = removeRoom(inv.invites, id)
	inv.joined = removeRoom(inv.joined, id)
	inv.fresh = removeRoom(inv.fresh, id)
	return inv.recombined()
}

// recombined rebuilds the union, invitations then fresh chats last.
func (inv inventory) recombined() inventory {
	all := make([]domain.Room, 0, len(inv.joined)+len(inv.invites)+len(inv.fresh))
	all = append(all, inv.joined...)
	all = append(all, inv.invites...)
	all = append(all, inv.fresh...)
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

// telegramLink is the joined or invited Telegram chat a tg:// link names, as each
// account in it sees it (domain.TelegramLinkRooms).
func (inv inventory) telegramLink(link domain.TelegramLink) []domain.Room {
	return domain.TelegramLinkRooms(link, inv.all)
}

// whatsAppLink is the WhatsApp chat with a link's number, as each account in it sees
// it (domain.WhatsAppLinkRooms).
func (inv inventory) whatsAppLink(link domain.WhatsAppLink) []domain.Room {
	return domain.WhatsAppLinkRooms(link, inv.all)
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
