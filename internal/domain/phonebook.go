package domain

import (
	"strings"
	"unicode"
)

// A bare number on one network is often named on another; the directory
// (directory.go) gathers every name. Numbers are international digits.

// NameRank is how much a name for a person is trusted; lower ranks first.
type NameRank int

const (
	// RankSaved is a name you saved: a phone's address book, a Telegram contact.
	RankSaved NameRank = iota
	// RankBridged is the name a bridge gives the person it fronts (the bridged
	// account's own contact name when it has one).
	RankBridged
	// RankChosen is a name the person chose for themselves: a WhatsApp push name or
	// business name, a Telegram profile that is not your contact.
	RankChosen
)

// PhoneIn is the digits of a label that is only an international number: "+",
// digits, spaces, dashes, dots or brackets, optionally followed by a bridge tag.
func PhoneIn(label string) (string, bool) {
	s := strings.TrimSpace(WithoutBridgeTag(label))
	rest, ok := strings.CutPrefix(s, "+")
	if !ok {
		return "", false
	}
	for _, r := range rest {
		if !unicode.IsDigit(r) && !strings.ContainsRune(" -.()", r) {
			return "", false
		}
	}
	digits := PhoneDigits(rest)
	if len(digits) < 7 || len(digits) > 15 {
		return "", false
	}
	return digits, true
}

// WithoutBridgeTag strips a trailing bridge tag such as " (WA)": two to four
// capitals in brackets.
func WithoutBridgeTag(name string) string {
	s := strings.TrimRight(name, " ")
	open := strings.LastIndex(s, " (")
	if open < 0 || !strings.HasSuffix(s, ")") {
		return name
	}
	tag := s[open+2 : len(s)-1]
	if len(tag) < 2 || len(tag) > 4 || strings.IndexFunc(tag, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return name
	}
	return s[:open]
}

// PhoneOf is the international digits in a WhatsApp person's ID, native
// ("whatsapp:<digits>@s.whatsapp.net") or bridged ("@whatsapp_[<instance>_]<digits>:server");
// empty otherwise.
func PhoneOf(userID string) string {
	id := ParseID(userID)
	var digits string
	switch {
	case id.Network == ProtocolWhatsApp && id.Account == "":
		user, server, _ := strings.Cut(id.Native, "@")
		if server != "s.whatsapp.net" {
			return ""
		}
		digits = user
	case id.Network == ProtocolMatrix && ProtocolOf(userID) == ProtocolWhatsApp:
		local := Localpart(userID)
		digits = local[strings.LastIndexByte(local, '_')+1:]
	default:
		return ""
	}
	if len(digits) < 7 || len(digits) > 15 || PhoneDigits(digits) != digits {
		return ""
	}
	return digits
}
