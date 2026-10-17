package signal

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Signal ID's account part is the account's ACI (see domain/network.go):
//
//	signal:<self>/<chat>                  a chat: the other side's ACI, or the group's ID
//	signal:<self>/<chat>/<author>.<sent>  a message (Signal's messages have no ID of their own)
//	signal:<person>                       a person: their ACI

// chatPart is a chat as an ID part. A group's ID comes as std base64, whose "/" is the
// ID separator: it is written base64url without padding instead.
func chatPart(chat string, group bool) string {
	if !group {
		return chat
	}
	raw, err := base64.StdEncoding.DecodeString(chat)
	if err != nil {
		return strings.NewReplacer("/", "_", "+", "-", "=", "").Replace(chat)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeGroup is a group's ID part back as the helper names the group (std base64).
func decodeGroup(part string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return "", fmt.Errorf("signal: %q is no group: %w", part, err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// isACI is whether part is an ACI (a UUID) rather than a group's ID.
func isACI(part string) bool {
	_, err := uuid.Parse(part)
	return err == nil && len(part) == 36
}

func roomID(self, chat string, group bool) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolSignal, self, chatPart(chat, group)))
}

func eventID(self, chat string, group bool, author string, sent uint64) domain.EventID {
	part := chatPart(chat, group) + "/" + author + "." + strconv.FormatUint(sent, 10)
	return domain.EventID(domain.NativeID(domain.ProtocolSignal, self, part))
}

func person(aci string) string { return domain.NativePerson(domain.ProtocolSignal, aci) }

// owner is the rooms account self owns.
func owner(self string) domain.RoomOwner { return domain.AccountRooms(domain.ProtocolSignal, self) }

// accountSpaceID is the space holding an account's rooms: the rail's home for them.
func accountSpaceID(self string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolSignal, self, "account"))
}

// selfOf is the account part of one of its rooms' IDs.
func selfOf(room domain.RoomID) (string, bool) {
	rest, ok := strings.CutPrefix(string(room), "signal:")
	if !ok {
		return "", false
	}
	self, _, ok := strings.Cut(rest, "/")
	return self, ok && self != ""
}
