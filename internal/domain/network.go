package domain

import "strings"

// IDs say their network. A Matrix ID stays bare, as Matrix writes it ("!room:server",
// "@user:server", "$event"), so every ID already in the cache, in a config or in a
// draft keeps meaning what it meant. Any other network's ID carries its network as a
// prefix, which a Matrix ID can never start with, because Matrix IDs start with a sigil:
//
//	whatsapp:<account>/<chat>   a room: one chat as one account sees it
//	whatsapp:<account>/<msg>    an event in that room
//	whatsapp:<person>           a person, whichever account sees them
//
// Rooms and events name the account because one person may run several accounts on a
// network: a group both are in is two rooms (each sends through its own account), and
// the same message reaches both with the same native ID. A person does not, because a
// person's network ID is the same whoever is looking, and that is what lets the same
// contact fold into one identity across accounts. <account> is the account's own
// stable ID on that network (for WhatsApp, its phone number's digits), never a label
// from the config, so renaming a label strands nothing.

// accountSep ends the account part. It never occurs in a native ID that kith keeps
// (a WhatsApp JID has '@', '.', and ':' for a device, never '/').
const accountSep = "/"

// native is what kith knows about one network that it reaches directly.
type native struct {
	protocol Protocol
	// isRoom tells a chat from a message within one account's IDs.
	isRoom func(nativeID string) bool
	// shortName is a person's readable fallback when they have no display name.
	shortName func(nativeID string) string
}

// natives are the networks kith reaches directly, keyed by their ID prefix.
var natives = map[string]native{
	"whatsapp": {
		protocol: ProtocolWhatsApp,
		// Every chat is a JID: "…@g.us", "…@s.whatsapp.net", "…@lid", "…@newsletter".
		// A message ID never has a server.
		isRoom: func(id string) bool { return strings.Contains(id, "@") },
		shortName: func(id string) string {
			user, server, _ := strings.Cut(id, "@")
			if server == "s.whatsapp.net" && user != "" {
				return "+" + user // a phone number, as people write it
			}
			return user
		},
	},
}

// prefixOf is the prefix that marks protocol's IDs, or "" for Matrix (bare) and for a
// network kith reaches only through a bridge.
func prefixOf(protocol Protocol) string {
	for prefix, n := range natives {
		if n.protocol == protocol {
			return prefix
		}
	}
	return ""
}

// ID is one kith ID taken apart.
type ID struct {
	// Network is the network the ID belongs to; Matrix for a bare ID.
	Network Protocol
	// Account is the account a room or event is seen through; empty for a person and
	// for every Matrix ID.
	Account string
	// Native is the ID as the network itself writes it.
	Native string
}

// ParseID takes an ID apart. Anything without a known network prefix is Matrix's, the
// whole string its native ID.
func ParseID(s string) ID {
	prefix, rest, ok := strings.Cut(s, ":")
	n, known := natives[prefix]
	if !ok || !known {
		return ID{Network: ProtocolMatrix, Native: s}
	}
	if account, id, scoped := strings.Cut(rest, accountSep); scoped {
		return ID{Network: n.protocol, Account: account, Native: id}
	}
	return ID{Network: n.protocol, Native: rest}
}

// NetworkOf is the network an ID belongs to: Matrix for a bare ID. It does not see
// through bridges; ProtocolOf does that for a person.
func NetworkOf(id string) Protocol { return ParseID(id).Network }

// IsMatrixUserID reports whether s has the shape of a Matrix user ID, "@name:server".
func IsMatrixUserID(s string) bool {
	return strings.HasPrefix(s, "@") && strings.Contains(s, ":")
}

// IsMatrixRoomID reports whether s has the shape of a Matrix room ID: "!opaque:server",
// or, from room version 12 on, "!opaque" with no server at all.
func IsMatrixRoomID(s string) bool {
	return len(s) > 1 && s[0] == '!'
}

// IsUserID reports whether s has the shape of a person's ID on any network.
func IsUserID(s string) bool {
	id := ParseID(s)
	if id.Network == ProtocolMatrix {
		return IsMatrixUserID(s)
	}
	return id.Account == "" && id.Native != ""
}

// IsRoomID reports whether s has the shape of a room ID on any network.
func IsRoomID(s string) bool {
	id := ParseID(s)
	if id.Network == ProtocolMatrix {
		return IsMatrixRoomID(s)
	}
	return id.Account != "" && natives[prefixOf(id.Network)].isRoom(id.Native)
}

// ShortName is the readable part of a person's ID, for when they have no display name:
// a Matrix ID's localpart, a WhatsApp phone number. Empty when the ID has none.
func ShortName(userID string) string {
	id := ParseID(userID)
	if id.Network == ProtocolMatrix {
		return Localpart(userID)
	}
	if id.Account != "" {
		return "" // a room or an event, not a person
	}
	return natives[prefixOf(id.Network)].shortName(id.Native)
}

// NativeID is a non-Matrix network's ID for something seen through account: a room or
// an event. A person's ID names no account (NativePerson).
func NativeID(network Protocol, account, native string) string {
	return prefixOf(network) + ":" + account + accountSep + native
}

// NativePerson is a person's ID on a non-Matrix network.
func NativePerson(network Protocol, native string) string {
	return prefixOf(network) + ":" + native
}

// RoomOwner names the rooms one writer keeps in the cache: Matrix's, or one account's
// on another network. Each writer reconciles only its own rooms, so one network's
// refresh can never sweep away another's history.
type RoomOwner string

// MatrixRooms owns every Matrix room (all Matrix room IDs start with "!").
const MatrixRooms RoomOwner = "!"

// AccountRooms owns the rooms one account sees on a non-Matrix network.
func AccountRooms(network Protocol, account string) RoomOwner {
	return RoomOwner(prefixOf(network) + ":" + account + accountSep)
}

// OwnerOf is the writer a room belongs to.
func OwnerOf(roomID RoomID) RoomOwner {
	id := ParseID(string(roomID))
	if id.Network == ProtocolMatrix {
		return MatrixRooms
	}
	return AccountRooms(id.Network, id.Account)
}

// Owns reports whether roomID is one of this writer's rooms.
func (o RoomOwner) Owns(roomID RoomID) bool { return o != "" && OwnerOf(roomID) == o }
