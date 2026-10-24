package domain

import (
	"encoding/base64"
	"strings"
)

// Signal addresses (signal.me and signal.group links, and their sgnl: forms) parsed
// into what the Signal adapter can look up: a person by phone number or by username
// link, or a group by its invite link.

// SignalLinkKind is what a Signal address names.
type SignalLinkKind int

const (
	// SignalLinkNone is the zero value: not a Signal address.
	SignalLinkNone SignalLinkKind = iota
	// SignalLinkPhone names a person by phone number.
	SignalLinkPhone
	// SignalLinkUsername names a person by a username link: Signal holds their username
	// encrypted, and the link carries the key.
	SignalLinkUsername
	// SignalLinkGroup names a group by its invite link.
	SignalLinkGroup
)

// usernameLinkSize is a username link's 32-byte key followed by its 16-byte handle.
const usernameLinkSize = 48

// SignalLink is one Signal address.
type SignalLink struct {
	Kind SignalLinkKind
	// Phone is the number's digits, without "+", for SignalLinkPhone.
	Phone string
	// Username is the link's key and handle, for SignalLinkUsername.
	Username []byte
	// Invite is the group's invite (a GroupInviteLink message), for SignalLinkGroup.
	Invite []byte
}

// ParseSignalLink reads one URI as a Signal address, and reports whether it is one:
// https://signal.me/#p/+N, https://signal.me/#eu/<link>, https://signal.group/#<invite>,
// and the same after sgnl:// in place of https://.
func ParseSignalLink(uri string) (SignalLink, bool) {
	uri = strings.TrimSpace(uri)
	for _, scheme := range []string{"https://", "http://", "sgnl://"} {
		if hasFoldedPrefix(uri, scheme) {
			uri = uri[len(scheme):]
			break
		}
	}
	host, fragment, ok := strings.Cut(uri, "#")
	if !ok {
		return SignalLink{}, false
	}
	switch strings.TrimSuffix(strings.ToLower(host), "/") {
	case "signal.me":
		if number, ok := strings.CutPrefix(fragment, "p/"); ok {
			digits := strings.TrimPrefix(number, "+")
			if len(digits) < 5 || !isPhoneDigits(digits) {
				return SignalLink{}, false
			}
			return SignalLink{Kind: SignalLinkPhone, Phone: digits}, true
		}
		if link, ok := strings.CutPrefix(fragment, "eu/"); ok {
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(link, "="))
			if err != nil || len(raw) != usernameLinkSize {
				return SignalLink{}, false
			}
			return SignalLink{Kind: SignalLinkUsername, Username: raw}, true
		}
	case "signal.group":
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(fragment, "="))
		if err != nil || len(raw) == 0 {
			return SignalLink{}, false
		}
		return SignalLink{Kind: SignalLinkGroup, Invite: raw}, true
	}
	return SignalLink{}, false
}
