package domain

import (
	"net/url"
	"strings"
)

// MessageLink is the link to event in room that its own network's apps open, where the
// network has one: matrix.to for Matrix, t.me/c for a Telegram channel or supergroup
// (which its members open). Anywhere else, and for an event that is not room's, it is
// the kith link (KithLink).
func MessageLink(room RoomID, event EventID) string {
	r, e := ParseID(string(room)), ParseID(string(event))
	if r.Network == "" || e.Network != r.Network || e.Account != r.Account {
		return KithLink(room, event)
	}
	switch r.Network {
	case ProtocolMatrix:
		if IsMatrixRoomID(string(room)) && IsMatrixEventID(string(event)) {
			return matrixToPrefix + url.PathEscape(r.Native) + "/" + url.PathEscape(e.Native)
		}
	case ProtocolTelegram:
		// A message is "<peer>/<id>"; only a -100<channel> peer has a message link
		// without a public @username.
		peer, id, _ := strings.Cut(e.Native, accountSep)
		channel, isChannel := strings.CutPrefix(peer, "-100")
		if peer == r.Native && isChannel && channel != "" && id != "" && allDigits(channel+id) {
			return "https://t.me/c/" + channel + "/" + id
		}
	default:
		// Slack's needs the workspace's address, which kith-mcp does not have; the rest
		// have no link to a message.
	}
	return KithLink(room, event)
}
