package instagram

import (
	"net/url"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An Instagram ID's account part is the account's own ID (see domain/network.go):
//
//	instagram:<self>/<thread>         a DM thread: its thread ID
//	instagram:<self>/<thread>/<msg>   a message in it (path-escaped)
//	instagram:<user>                  a person: their user ID

func roomID(self, thread string) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolInstagram, self, thread))
}

func eventID(self, thread, message string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolInstagram, self, thread+"/"+url.PathEscape(message)))
}

// owner is the rooms account self owns.
func owner(self string) domain.RoomOwner {
	return domain.AccountRooms(domain.ProtocolInstagram, self)
}

// accountSpaceID is the space holding an account's rooms: the rail's home for them.
func accountSpaceID(self string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolInstagram, self, "account"))
}

// threadOf is a room's account and thread.
func threadOf(room domain.RoomID) (self, thread string, ok bool) {
	id := domain.ParseID(string(room))
	if id.Network != domain.ProtocolInstagram || id.Account == "" || id.Native == "" || strings.Contains(id.Native, "/") {
		return "", "", false
	}
	return id.Account, id.Native, true
}

// messageOf is the Instagram message an event's ID names.
func messageOf(event domain.EventID) (string, bool) {
	id := domain.ParseID(string(event))
	if id.Network != domain.ProtocolInstagram {
		return "", false
	}
	_, escaped, ok := strings.Cut(id.Native, "/")
	if !ok {
		return "", false
	}
	message, err := url.PathUnescape(escaped)
	return message, err == nil && message != ""
}
