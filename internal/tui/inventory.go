package tui

import (
	"slices"
	"strconv"

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

// telegramLink is the joined or invited Telegram chat a tg:// link names, by its
// stored @username, or by its channel ID for a private channel. Several accounts may
// see the same chat; the first is picked, since the chat the user wants to open is
// the same.
func (inv inventory) telegramLink(link domain.TelegramLink) (domain.Room, bool) {
	for i := range inv.all {
		r := &inv.all[i]
		if domain.NetworkOf(string(r.ID)) != domain.ProtocolTelegram {
			continue
		}
		switch link.Kind {
		case domain.TelegramLinkUsername:
			if r.Handle == link.Username {
				return *r, true
			}
		case domain.TelegramLinkPrivateChannel:
			if link.Channel == 0 {
				return domain.Room{}, false
			}
			parsed := domain.ParseID(string(r.ID))
			if parsed.Native == "-"+telegramPrivateMark(link.Channel) {
				return *r, true
			}
		case domain.TelegramLinkPhone, domain.TelegramLinkInvite, domain.TelegramLinkNone:
			// A number or an invite names no listed chat: Telegram is asked.
		}
	}
	return domain.Room{}, false
}

// telegramPrivateMark is a private channel's native ID without the leading minus (the
// Bot API's "-100<id>" without the sign), matching how Telegram chats are stored
// (telegram/rooms.go: channelMark).
func telegramPrivateMark(channel int64) string {
	return strconv.FormatInt(1_000_000_000_000+channel, 10)
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
