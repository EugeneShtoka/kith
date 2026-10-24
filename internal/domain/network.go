package domain

import "strings"

// A Matrix ID stays bare ("!room:server", "@user:server", "$event"). Any other
// network's ID carries a network prefix, which a Matrix ID (starting with a sigil)
// never has:
//
//	whatsapp:<account>/<chat>   a room: one chat as one account sees it
//	whatsapp:<account>/<msg>    an event in that room
//	whatsapp:<person>           a person, whichever account sees them
//	slack:<team>/<channel>      a channel, DM or group DM in one workspace
//	slack:<team>/<channel>/<ts> a message in it (a ts is unique only per channel)
//	slack:<team>.<user>         a person (a Slack user ID is unique only per workspace)
//	telegram:<self>/<peer>      a chat, group or channel as one account sees it
//	telegram:<self>/<peer>/<id> a message in it (a message ID is unique only per chat)
//	telegram:<user>             a person
//	messenger:<self>/<thread>   a Messenger thread (a Lightspeed thread key), or an
//	                            encrypted chat (its WhatsApp-protocol JID, "…@msgr")
//	messenger:<self>/<thread>/<mid> a message in it
//	messenger:<person>          a person (a Meta user ID)
//	linkedin:<self>/<thread>    a LinkedIn conversation (its thread ID, path-escaped)
//	linkedin:<self>/<thread>/<msg> a message in it (path-escaped)
//	linkedin:<person>           a person or an organization (a profile ID)
//	gmessages:<phone>/<conv>    a Google Messages conversation, as one paired phone
//	                            sees it (a hash of the phone's ID; the conversation
//	                            path-escaped)
//	gmessages:<phone>/<conv>/<id> a message in it
//	gmessages:+<number>         a person with a phone number
//	signal:<self>/<chat>        a Signal chat as one account sees it: the other side's
//	                            ACI, or a group's ID (base64url, no padding)
//	signal:<self>/<chat>/<author>.<millis> a message in it (Signal messages have no
//	                            ID of their own: the author and its sent time)
//	signal:<person>             a person (an ACI)
//	instagram:<self>/<thread>   an Instagram DM thread (its thread ID)
//	instagram:<self>/<thread>/<msg> a message in it (path-escaped)
//	instagram:<person>          a person (an Instagram user ID)
//
// Rooms and events name the account because one person may run several accounts on a
// network, and a shared group is then two rooms with the same native message IDs. A
// person's ID names no account, so a contact folds into one identity across accounts.
// <account> is the account's stable network ID (WhatsApp phone digits, Slack team ID,
// Telegram user ID, Meta user ID, LinkedIn profile ID, paired phone ID, Signal ACI, Instagram user ID), never a config label.
//
// A Telegram <peer> is "marked" as in the Bot API (user ID, basic group negated,
// channel/supergroup -100<id>) because the three kinds' IDs overlap.

// accountSep ends the account part. No kept native ID contains it, except as Slack's
// and Telegram's chat/message separator after the account part.
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
	"slack": {
		protocol: ProtocolSlack,
		// A channel ID is letters and digits ("C…", "G…", "D…"); a message is
		// "<channel>/<ts>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is "<team>.<user>": neither part reads as a name.
		shortName: func(string) string { return "" },
	},
	"messenger": {
		protocol: ProtocolMessenger,
		// A thread key is digits and a chat JID has no slash; a message is
		// "<thread>/<mid>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is a Meta user ID, which reads as no name.
		shortName: func(string) string { return "" },
	},
	"linkedin": {
		protocol: ProtocolLinkedIn,
		// A thread ID is path-escaped, so it has no slash; a message is "<thread>/<msg>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is a profile ID ("ACoAA…"), which reads as no name.
		shortName: func(string) string { return "" },
	},
	"gmessages": {
		protocol: ProtocolGMessages,
		// A conversation ID is path-escaped, so it has no slash; a message is
		// "<conv>/<id>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person with a number is "+<number>", as people write it; anyone else is the
		// phone's own ID for them, which reads as no name.
		shortName: func(id string) string {
			if strings.HasPrefix(id, "+") {
				return id
			}
			return ""
		},
	},
	"telegram": {
		protocol: ProtocolTelegram,
		// A chat is a marked peer ID ("123", "-456", "-100789"); a message is
		// "<peer>/<id>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is a user ID, which reads as no name.
		shortName: func(string) string { return "" },
	},
	"instagram": {
		protocol: ProtocolInstagram,
		// A thread ID is digits; a message is "<thread>/<msg>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is an Instagram user ID, which reads as no name.
		shortName: func(string) string { return "" },
	},
	"signal": {
		protocol: ProtocolSignal,
		// A chat is an ACI or a base64url group ID, neither with a slash; a message is
		// "<chat>/<author>.<millis>".
		isRoom: func(id string) bool { return id != "" && !strings.Contains(id, accountSep) },
		// A person is an ACI (a UUID), which reads as no name.
		shortName: func(string) string { return "" },
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
	// for a bare Matrix ID.
	Account string
	// Native is the ID as the network itself writes it.
	Native string
}

// matrixPrefix marks a Matrix ID in the form every network's IDs take:
// "matrix:@user:server" for a person, "matrix:<account>/<native>" for a room or event.
// Until Matrix IDs are written that way, a bare ID is Matrix's too.
const matrixPrefix = "matrix:"

// ParseID takes an ID apart. Anything without a known network prefix is Matrix's, the
// whole string its native ID.
func ParseID(s string) ID {
	if id, ok := parseMatrixID(s); ok {
		return id
	}
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

// parseMatrixID takes apart a Matrix ID that carries matrixPrefix. A person's native ID
// starts with "@" and is never split; anything else needs an AccountKey before the
// separator, so a matrix: URI ("matrix:u/…", "matrix:roomid/…") never reads as an ID.
func parseMatrixID(s string) (ID, bool) {
	rest, ok := strings.CutPrefix(s, matrixPrefix)
	if !ok {
		return ID{}, false
	}
	if strings.HasPrefix(rest, "@") {
		return ID{Network: ProtocolMatrix, Native: rest}, true
	}
	account, native, ok := strings.Cut(rest, accountSep)
	if !ok || native == "" || !isAccountKey(account) {
		return ID{}, false
	}
	return ID{Network: ProtocolMatrix, Account: account, Native: native}, true
}

// MatrixNative is a Matrix ID as Matrix writes it, from kith's form with or without
// matrixPrefix. Anything else comes back as it is.
func MatrixNative(s string) string {
	if id, ok := parseMatrixID(s); ok {
		return id.Native
	}
	return s
}

// NetworkOf is the network an ID belongs to: Matrix for a bare ID. It does not see
// through bridges; ProtocolOf does that for a person.
func NetworkOf(id string) Protocol { return ParseID(id).Network }

// IsMatrixUserID reports whether s has the shape of a Matrix user ID, "@name:server".
func IsMatrixUserID(s string) bool {
	id := ParseID(s)
	return id.Network == ProtocolMatrix && id.Account == "" &&
		strings.HasPrefix(id.Native, "@") && strings.Contains(id.Native, ":")
}

// IsMatrixRoomID reports whether s has the shape of a Matrix room ID: "!opaque:server",
// or, from room version 12 on, "!opaque" with no server at all.
func IsMatrixRoomID(s string) bool {
	id := ParseID(s)
	return id.Network == ProtocolMatrix && len(id.Native) > 1 && id.Native[0] == '!'
}

// IsMatrixEventID reports whether s has the shape of a Matrix event ID, "$opaque".
func IsMatrixEventID(s string) bool {
	id := ParseID(s)
	return id.Network == ProtocolMatrix && len(id.Native) > 1 && id.Native[0] == '$'
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

// RoomOwner names the rooms one cache writer owns: Matrix's, or one account's. Each
// writer reconciles only its own rooms, so one network's refresh never sweeps another's.
type RoomOwner string

// MatrixRooms owns every Matrix room (all Matrix room IDs start with "!").
const MatrixRooms RoomOwner = "!"

// AccountRooms owns the rooms one account sees on a non-Matrix network.
func AccountRooms(network Protocol, account string) RoomOwner {
	return RoomOwner(prefixOf(network) + ":" + account + accountSep)
}

// NetworkRooms is the prefix of every room of network's accounts, for a query over all
// of them (its Owns matches none: a room is owned by its account).
func NetworkRooms(network Protocol) RoomOwner {
	return RoomOwner(prefixOf(network) + ":")
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
