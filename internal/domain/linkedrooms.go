package domain

import (
	"slices"
	"strconv"
)

// Followable reports whether kith follows link rather than the browser: a Matrix address,
// a Telegram, WhatsApp or Signal chat link, or a kith link (its room's network), on a
// network one of rooms is on (a bridged chat is Matrix's, which looks up no Telegram link).
func Followable(link string, rooms []Room) bool {
	network, ok := LinkNetwork(link)
	if _, place := ParsePlace(link); place {
		network, ok = ProtocolMatrix, true
	}
	if place, kith := ParseKithLink(link); kith {
		network, ok = NetworkOf(place.Room), true
	}
	return ok && slices.ContainsFunc(rooms, func(r Room) bool {
		return NetworkOf(string(r.ID)) == network
	})
}

// LinkedRooms is the chat a link names among rooms, as each account in it sees it: a
// Matrix room by its ID, a Telegram chat by its @username or private channel, a
// WhatsApp chat by its number, a kith link's room by its kith ID. None when the link
// names no listed chat (an alias, an invite, a person), which only its network can look up.
func LinkedRooms(uri string, rooms []Room) []Room {
	if place, ok := ParseKithLink(uri); ok {
		return kithLinkRooms(place, rooms)
	}
	if place, ok := ParsePlace(uri); ok {
		if place.Kind != PlaceRoom && place.Kind != PlaceEvent {
			return nil
		}
		return MatrixPlaceRooms(place, rooms)
	}
	if link, ok := ParseTelegramLink(uri); ok {
		return TelegramLinkRooms(link, rooms)
	}
	if link, ok := ParseWhatsAppLink(uri); ok {
		return WhatsAppLinkRooms(link, rooms)
	}
	return nil
}

// LinkedMessage is the message a link names in room, one of its LinkedRooms: a Matrix
// event as that room's account sees it, a Telegram post, or a kith link's event in its
// own room; "" for a link to the chat.
func LinkedMessage(uri string, room RoomID) EventID {
	if place, ok := ParseKithLink(uri); ok {
		if RoomID(place.Room) != room {
			return ""
		}
		return place.Event
	}
	if place, ok := ParsePlace(uri); ok {
		if place.Kind != PlaceEvent {
			return ""
		}
		return TypedEventID(room, string(place.Event))
	}
	if link, ok := ParseTelegramLink(uri); ok && link.Post > 0 {
		return EventID(string(room) + "/" + strconv.Itoa(link.Post))
	}
	return ""
}

// MatrixPlaceRooms is the Matrix room a place names, by its ID: kith's, or Matrix's own
// as each account sees it.
func MatrixPlaceRooms(place Place, rooms []Room) []Room {
	for i := range rooms {
		if rooms[i].ID == RoomID(place.Room) {
			return []Room{rooms[i]}
		}
	}
	var out []Room
	for i := range rooms {
		if id := string(rooms[i].ID); IsMatrixRoomID(id) && MatrixNative(id) == place.Room {
			out = append(out, rooms[i])
		}
	}
	return out
}

// TelegramLinkRooms is the Telegram chat a tg:// link names, by its stored @username,
// or by its channel ID for a private channel, as each account in it sees it.
func TelegramLinkRooms(link TelegramLink, rooms []Room) []Room {
	var out []Room
	for i := range rooms {
		r := &rooms[i]
		if NetworkOf(string(r.ID)) != ProtocolTelegram {
			continue
		}
		switch link.Kind {
		case TelegramLinkUsername:
			if r.Handle == link.Username {
				out = append(out, *r)
			}
		case TelegramLinkPrivateChannel:
			parsed := ParseID(string(r.ID))
			if link.Channel != 0 && parsed.Native == "-"+telegramPrivateMark(link.Channel) {
				out = append(out, *r)
			}
		case TelegramLinkPhone, TelegramLinkInvite, TelegramLinkNone:
			// A number or an invite names no listed chat: Telegram is asked.
		}
	}
	return out
}

// WhatsAppLinkRooms is the WhatsApp chat with a link's number, as each account in it
// sees it: its room's native ID is the number's phone JID (whatsapp/rooms.go). An
// invite names no listed chat.
func WhatsAppLinkRooms(link WhatsAppLink, rooms []Room) []Room {
	if link.Kind != WhatsAppLinkPhone {
		return nil
	}
	var out []Room
	for i := range rooms {
		parsed := ParseID(string(rooms[i].ID))
		if parsed.Network == ProtocolWhatsApp && parsed.Native == link.Phone+"@s.whatsapp.net" {
			out = append(out, rooms[i])
		}
	}
	return out
}

// telegramPrivateMark is a private channel's native ID without the leading minus (the
// Bot API's "-100<id>" without the sign), matching how Telegram chats are stored
// (telegram/rooms.go: channelMark).
func telegramPrivateMark(channel int64) string {
	return strconv.FormatInt(1_000_000_000_000+channel, 10)
}
