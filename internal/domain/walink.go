package domain

import (
	"net/url"
	"strings"
)

// WhatsApp addresses (whatsapp: URIs, and the wa.me and chat.whatsapp.com web forms
// people paste) parsed into what a WhatsApp adapter can look up: a person by phone
// number, or a group by its invite code.

// WhatsAppLinkKind is what a WhatsApp address names.
type WhatsAppLinkKind int

const (
	// WhatsAppLinkNone is the zero value: not a WhatsApp address.
	WhatsAppLinkNone WhatsAppLinkKind = iota
	// WhatsAppLinkPhone names a person by phone number.
	WhatsAppLinkPhone
	// WhatsAppLinkInvite names a group by its invite code.
	WhatsAppLinkInvite
)

// WhatsAppLink is one WhatsApp address.
type WhatsAppLink struct {
	Kind WhatsAppLinkKind
	// Phone is the number's digits, without "+", for a person.
	Phone string
	// Invite is the invite code, for a group.
	Invite string
	// Text is a message the link suggests writing (its "text" parameter), "" for none.
	Text string
}

// ParseWhatsAppLink reads one URI as a WhatsApp address, and reports whether it is
// one. It accepts whatsapp://send?phone=N[&text=T] and whatsapp://chat?code=C, and
// their web forms: wa.me/N, api.whatsapp.com/send?phone=N (or web.whatsapp.com), and
// chat.whatsapp.com/C. A link naming nobody (a share with only text, a business's
// wa.me/message link) is not one kith can follow.
func ParseWhatsAppLink(uri string) (WhatsAppLink, bool) {
	uri = strings.TrimSpace(uri)
	if hasFoldedPrefix(uri, "whatsapp:") {
		rest := strings.TrimPrefix(uri[len("whatsapp:"):], "//")
		head, query, _ := strings.Cut(rest, "?")
		values, err := url.ParseQuery(query)
		if err != nil {
			return WhatsAppLink{}, false
		}
		switch strings.ToLower(strings.TrimSuffix(head, "/")) {
		case "send":
			return whatsAppPhone(values.Get("phone"), values.Get("text"))
		case "chat":
			return whatsAppInvite(values.Get("code"))
		}
		return WhatsAppLink{}, false
	}
	for _, scheme := range []string{"https://", "http://"} {
		if hasFoldedPrefix(uri, scheme) {
			uri = uri[len(scheme):]
			break
		}
	}
	host, rest, _ := strings.Cut(uri, "/")
	rest, _, _ = strings.Cut(rest, "#")
	path, query, _ := strings.Cut(rest, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		values = url.Values{} // the text is a suggestion; a bad query loses only it
	}
	path = strings.TrimSuffix(path, "/")
	switch strings.TrimPrefix(strings.ToLower(host), "www.") {
	case "wa.me":
		return whatsAppPhone(path, values.Get("text"))
	case "api.whatsapp.com", "web.whatsapp.com":
		if !strings.EqualFold(path, "send") {
			return WhatsAppLink{}, false
		}
		return whatsAppPhone(values.Get("phone"), values.Get("text"))
	case "chat.whatsapp.com":
		return whatsAppInvite(strings.TrimPrefix(path, "invite/"))
	}
	return WhatsAppLink{}, false
}

// whatsAppPhone is a link to the person at number, as people write it: a leading "+",
// spaces, dashes, dots and brackets are dropped.
func whatsAppPhone(number, text string) (WhatsAppLink, bool) {
	digits := strings.TrimPrefix(strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -.()", r) {
			return -1
		}
		return r
	}, number), "+")
	if len(digits) < 5 || !isPhoneDigits(digits) {
		return WhatsAppLink{}, false
	}
	return WhatsAppLink{Kind: WhatsAppLinkPhone, Phone: digits, Text: text}, true
}

// whatsAppInvite is a link to the group code invites to.
func whatsAppInvite(code string) (WhatsAppLink, bool) {
	if !isInviteHash(code) {
		return WhatsAppLink{}, false
	}
	return WhatsAppLink{Kind: WhatsAppLinkInvite, Invite: code}, true
}
