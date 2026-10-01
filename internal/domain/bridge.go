package domain

import (
	"strings"
	"time"
)

// Protocol is the messaging network a person is actually on.
type Protocol string

// String returns the network's human-facing name ("WhatsApp", "Matrix", …).
func (p Protocol) String() string { return string(p) }

// IsBridged reports whether the person (or room) is on a network other than Matrix:
// reached through a bridge, or directly by an adapter. The zero value is not known to
// be anywhere else, so it is not.
func (p Protocol) IsBridged() bool { return p != "" && p != ProtocolMatrix }

// RepliesAreThreads reports whether the network has no reply that is not a thread.
func (p Protocol) RepliesAreThreads() bool { return p == ProtocolSlack }

// The networks we can name. Anyone we can't attribute to a bridge is Matrix.
const (
	ProtocolMatrix     Protocol = "Matrix"
	ProtocolWhatsApp   Protocol = "WhatsApp"
	ProtocolTelegram   Protocol = "Telegram"
	ProtocolSignal     Protocol = "Signal"
	ProtocolSlack      Protocol = "Slack"
	ProtocolDiscord    Protocol = "Discord"
	ProtocolMessenger  Protocol = "Messenger"
	ProtocolInstagram  Protocol = "Instagram"
	ProtocolLinkedIn   Protocol = "LinkedIn"
	ProtocolGMessages  Protocol = "Google Messages"
	ProtocolGoogleChat Protocol = "Google Chat"
	ProtocolIMessage   Protocol = "iMessage"
	ProtocolTwitter    Protocol = "Twitter"
)

// bridges maps a bridge's localpart token to the network it fronts.
var bridges = map[string]Protocol{
	"whatsapp":   ProtocolWhatsApp,
	"telegram":   ProtocolTelegram,
	"signal":     ProtocolSignal,
	"slack":      ProtocolSlack,
	"discord":    ProtocolDiscord,
	"meta":       ProtocolMessenger, // mautrix-meta fronts Messenger…
	"messenger":  ProtocolMessenger,
	"facebook":   ProtocolMessenger,
	"instagram":  ProtocolInstagram, // …and Instagram, when deployed separately
	"linkedin":   ProtocolLinkedIn,
	"gmessages":  ProtocolGMessages,
	"googlechat": ProtocolGoogleChat,
	"imessage":   ProtocolIMessage,
	"twitter":    ProtocolTwitter,
}

// ProtocolOf is the network a person is actually on: the one their ID names, or, for a
// Matrix ID, the one the bridge it belongs to fronts.
func ProtocolOf(mxid string) Protocol {
	if p := NetworkOf(mxid); p != ProtocolMatrix {
		return p
	}
	local := strings.TrimPrefix(mxid, "@")
	if colon := strings.IndexByte(local, ':'); colon >= 0 {
		local = local[:colon]
	}
	local = strings.ToLower(strings.TrimPrefix(local, "_"))

	// A ghost user: the token is everything before the first separator.
	if sep := strings.IndexByte(local, '_'); sep > 0 {
		if p, ok := bridges[local[:sep]]; ok {
			return p
		}
	}
	if p, ok := botProtocol(local); ok {
		return p
	}
	return ProtocolMatrix
}

// botProtocol recognizes a bridge's own bot from its localpart: the network's token,
// then "bot", then optionally an instance name.
func botProtocol(local string) (Protocol, bool) {
	for token, p := range bridges {
		rest, ok := strings.CutPrefix(local, token+"bot")
		if !ok {
			continue
		}
		if rest == "" || rest[0] == '_' || rest[0] == '-' || rest[0] == '.' {
			return p, true
		}
	}
	return ProtocolMatrix, false
}

// ReactionRefusal is an emoji a bridged network would not accept as a reaction, and
// when that was last observed.
type ReactionRefusal struct {
	Protocol string
	Emoji    string
	At       time.Time
}
