package linkedin

import (
	"net/url"
	"strings"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// LinkedIn names everything by URN:
//
//	conversation  urn:li:msg_conversation:(urn:li:fsd_profile:<me>,<thread>)
//	message       urn:li:msg_message:(urn:li:fsd_profile:<…>,<mid>)
//	person        urn:li:fsd_profile:<profile>, or a participant URN ending in it
//
// kith's IDs keep the parts that change: linkedin:<self>/<thread> for a room and
// linkedin:<self>/<thread>/<message> for an event, where <message> is the message
// URN's parenthesised ID. Thread and message IDs may hold a slash, so each part is
// path-escaped, and the URNs are rebuilt from them.

// esc is one part of a kith ID; unesc undoes it.
func esc(s string) string { return url.PathEscape(s) }

func unesc(s string) (string, bool) {
	out, err := url.PathUnescape(s)
	return out, err == nil
}

// roomID is a conversation's room.
func roomID(self, thread string) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolLinkedIn, self, esc(thread)))
}

// eventID is a message in a conversation, by its URN's ID ("(urn:li:…,<mid>)").
func eventID(self, thread, message string) domain.EventID {
	return domain.EventID(domain.NativeID(domain.ProtocolLinkedIn, self, esc(thread)+"/"+esc(message)))
}

// accountSpaceID is the space an account is.
func accountSpaceID(self string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolLinkedIn, self, "account"))
}

// owner is the rooms one account writes.
func owner(self string) domain.RoomOwner { return domain.AccountRooms(domain.ProtocolLinkedIn, self) }

// threadOf is a room's account and thread.
func threadOf(room domain.RoomID) (self, thread string, ok bool) {
	id := domain.ParseID(string(room))
	if id.Network != domain.ProtocolLinkedIn || id.Account == "" || strings.Contains(id.Native, "/") {
		return "", "", false
	}
	thread, ok = unesc(id.Native)
	return id.Account, thread, ok && thread != ""
}

// threadIn is a conversation URN's thread, the part after the mailbox in its
// parenthesised ID; ok is false for a URN of another shape.
func threadIn(conversation linkedingo.URN) (string, bool) {
	inner, ok := parenthesised(conversation.ID())
	if !ok {
		return "", false
	}
	i := strings.LastIndex(inner, ",")
	if i < 0 || i == len(inner)-1 {
		return "", false
	}
	return inner[i+1:], true
}

// parenthesised is s without the parentheses around it.
func parenthesised(s string) (string, bool) {
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return "", false
	}
	return s[1 : len(s)-1], true
}

// conversationURN is a thread's conversation as LinkedIn names it, in self's mailbox.
func conversationURN(self, thread string) linkedingo.URN {
	return linkedingo.NewURN("urn:li:msg_conversation:(urn:li:fsd_profile:" + self + "," + thread + ")")
}

// profileOf is the profile a participant URN ends in ("urn:li:msg_messagingParticipant:
// urn:li:fsd_profile:<profile>", or the profile URN itself).
func profileOf(participant linkedingo.URN) string { return participant.ID() }
