package gmessages

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// kith's IDs for Google Messages:
//
//	gmessages:<self>/<conversation>       a conversation, as one paired phone sees it
//	gmessages:<self>/<conversation>/<id>  a message in it
//	gmessages:<self>/<conversation>/<id>/<participant>  a participant's reaction to it
//	gmessages:+<number>                   a person with a phone number
//	gmessages:<self>.<participant>        anyone else, as the phone numbers them
//
// <self> is a short hash of the Google account the phone pairs as. Google writes the
// paired phone's ID as "<account address>/<number>", and the number is new with every
// pairing: only the account part is hashed, so pairing again keeps the same rooms. The
// hash has no address in it and no slash. Conversation and message IDs are the phone's
// own, path-escaped. A person with a number is named by it, so the same number on
// another network can be the same person.

// selfOf is the account part of IDs for the phone paired as phoneID.
func selfOf(phoneID string) string {
	account := phoneID
	if i := strings.LastIndex(phoneID, "/"); i > 0 {
		account = phoneID[:i]
	}
	sum := sha256.Sum256([]byte(account))
	return hex.EncodeToString(sum[:6])
}

func esc(s string) string { return url.PathEscape(s) }

// roomID is a conversation's room.
func roomID(self, conversation string) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolGMessages, self, esc(conversation)))
}

// eventID is a message in a conversation.
func eventID(self, conversation, message string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolGMessages, self, esc(conversation)+"/"+esc(message)))
}

// accountSpaceID is the space an account is.
func accountSpaceID(self string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolGMessages, self, "account"))
}

// owner is the rooms one account writes.
func owner(self string) domain.RoomOwner { return domain.AccountRooms(domain.ProtocolGMessages, self) }

// conversationOf is a room's account and conversation.
func conversationOf(room domain.RoomID) (self, conversation string, ok bool) {
	id := domain.ParseID(string(room))
	if id.Network != domain.ProtocolGMessages || id.Account == "" || strings.Contains(id.Native, "/") {
		return "", "", false
	}
	conversation, err := url.PathUnescape(id.Native)
	return id.Account, conversation, err == nil && conversation != ""
}

// reactionID is a participant's reaction to a message: one each, as Google Messages
// allows.
func reactionID(self, conversation, message, participant string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolGMessages, self,
		esc(conversation)+"/"+esc(message)+"/"+esc(participant)))
}

// messageOf is an event's message ID on the phone; a reaction is not a message.
func messageOf(event domain.EventID) (string, bool) {
	id := domain.ParseID(string(event))
	_, m, cut := strings.Cut(id.Native, "/")
	if id.Network != domain.ProtocolGMessages || !cut || strings.Contains(m, "/") {
		return "", false
	}
	message, err := url.PathUnescape(m)
	return message, err == nil && message != ""
}

// personOf is a participant's kith ID: their number, else the phone's own ID for them.
func personOf(self string, p *gmproto.SmallInfo) string {
	if n := strings.TrimSpace(p.GetNumber()); n != "" && p.GetType() == gmproto.IdentifierType_PHONE {
		return domain.NativePerson(domain.ProtocolGMessages, n)
	}
	return domain.NativePerson(domain.ProtocolGMessages, self+"."+p.GetParticipantID())
}
