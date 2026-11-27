package domain

import (
	"net/url"
	"strings"
)

// MessageLink is the link to event in room that its own network's apps open, where the
// network has one: matrix.to for Matrix, t.me/<username> for a public Telegram channel
// or supergroup (which anyone opens) and t.me/c for a private one (which its members
// open), and for Slack the archives link under web, the workspace's
// address (Account.Web, "acme.slack.com"), or its web client's by team ID while that is
// unknown. Anywhere else, and for an event that is not room's, it is the kith link
// (KithLink).
func MessageLink(room Room, event EventID, web string) string {
	r, e := ParseID(string(room.ID)), ParseID(string(event))
	if r.Network == "" || e.Network != r.Network || e.Account != r.Account {
		return KithLink(room.ID, event)
	}
	switch r.Network {
	case ProtocolMatrix:
		if IsMatrixRoomID(string(room.ID)) && IsMatrixEventID(string(event)) {
			return matrixToPrefix + url.PathEscape(r.Native) + "/" + url.PathEscape(e.Native)
		}
	case ProtocolTelegram:
		// A message is "<peer>/<id>"; only a -100<channel> peer has a message link.
		peer, id, _ := strings.Cut(e.Native, accountSep)
		channel, isChannel := strings.CutPrefix(peer, "-100")
		if peer == r.Native && isChannel && channel != "" && id != "" && allDigits(channel+id) {
			if room.Handle != "" {
				return "https://t.me/" + room.Handle + "/" + id
			}
			return "https://t.me/c/" + channel + "/" + id
		}
	case ProtocolSlack:
		// A message is "<channel>/<ts>"; the link drops the ts's dot.
		channel, ts, _ := strings.Cut(e.Native, accountSep)
		if channel == r.Native && ts != "" {
			path := "/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
			if web != "" {
				return "https://" + web + "/archives" + path
			}
			return "https://app.slack.com/client/" + r.Account + path
		}
	default:
		// The rest have no link to a message.
	}
	return KithLink(room.ID, event)
}
