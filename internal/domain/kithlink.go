package domain

import (
	"net/url"
	"strings"
)

// kith's own links name a room, or a message in it, by kith's IDs on any network:
//
//	kith:<room>[?event=<event>]
//
// <room> is a room ID path-escaped (url.PathEscape: its "/" becomes "%2F"), <event> an
// event ID in it query-escaped (url.QueryEscape), both as kith writes them
// ("whatsapp:<account>/<chat>", "slack:<team>/<channel>/<ts>"): the IDs kith-mcp
// hands out. Such a link names one account's view of the chat, so only that account's
// room follows it.

// kithScheme starts a kith link.
const kithScheme = "kith:"

// ParseKithLink reads one URI as a kith link, and reports whether it is one: a room ID
// on a known network, and an event of the same account's when it names one.
func ParseKithLink(uri string) (Place, bool) {
	uri = strings.TrimSpace(uri)
	if !hasFoldedPrefix(uri, kithScheme) {
		return Place{}, false
	}
	head, query, _ := strings.Cut(uri[len(kithScheme):], "?")
	room, err := url.PathUnescape(head)
	if err != nil || !IsRoomID(room) {
		return Place{}, false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return Place{}, false
	}
	events, hasEvent := values["event"]
	if !hasEvent {
		return Place{Kind: PlaceRoom, Room: room}, true
	}
	event := events[0]
	if len(events) > 1 || IsRoomID(event) || OwnerOf(RoomID(event)) != OwnerOf(RoomID(room)) {
		return Place{}, false // two messages, none, or one no account of the room's sees
	}
	return Place{Kind: PlaceEvent, Room: room, Event: EventID(event)}, true
}

// kithLinkRooms is the room a kith link names among rooms: that exact room, or none.
func kithLinkRooms(place Place, rooms []Room) []Room {
	for i := range rooms {
		if rooms[i].ID == RoomID(place.Room) {
			return []Room{rooms[i]}
		}
	}
	return nil
}
