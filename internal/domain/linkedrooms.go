package domain

import "strconv"

// LinkedRooms is the chat a link names among rooms, as each account in it sees it: a
// Matrix room by its ID, a Telegram chat by its @username or private channel, a
// WhatsApp chat by its number. None when the link names no listed chat (an alias, an
// invite, a person), which only its network can look up.
func LinkedRooms(uri string, rooms []Room) []Room {
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
