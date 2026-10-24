package domain

import (
	"net/url"
	"strconv"
	"strings"
)

// Telegram addresses (tg: URIs, with t.me's web form kept for completeness) parsed into
// chats a Telegram adapter can look up: a public chat or person by @username, a person
// by phone number, a chat by its invite, or a private channel by its ID.

// TelegramLinkKind is what a Telegram address names.
type TelegramLinkKind int

const (
	// TelegramLinkNone is the zero value: not a Telegram address.
	TelegramLinkNone TelegramLinkKind = iota
	// TelegramLinkUsername names a public chat by its @username; a channel or group
	// alike.
	TelegramLinkUsername
	// TelegramLinkPrivateChannel names a private channel by its ID (as the Bot API's
	// -100<id>, before marking).
	TelegramLinkPrivateChannel
	// TelegramLinkPhone names a person by phone number.
	TelegramLinkPhone
	// TelegramLinkInvite names a group or channel by its invite hash.
	TelegramLinkInvite
)

// TelegramLink is one Telegram address: what it names, and the message in it if the
// link named one.
type TelegramLink struct {
	Kind TelegramLinkKind
	// Username is the @name (lowercased, without "@"), for a public chat.
	Username string
	// Channel is the raw channel ID (without the -100 mark), for a private channel.
	Channel int64
	// Phone is the number's digits, without "+", for a person by phone.
	Phone string
	// Invite is the invite hash, for a chat joined by invitation.
	Invite string
	// Post is the message ID in the chat, 0 when the link named only the chat.
	Post int
	// Text is a message the link suggests writing (its "text" parameter), "" for none.
	Text string
}

// ParseTelegramLink reads one URI as a Telegram address, and reports whether it is one.
// It accepts the tg: forms the Telegram apps write — tg://resolve?domain=X[&post=N],
// tg://resolve?phone=N, tg://join?invite=H and tg://privatepost?channel=X&post=N — and
// their https://t.me equivalents (t.me/X[/N], t.me/+N, t.me/+H or t.me/joinchat/H,
// t.me/c/X/N). A "text" parameter is kept as the message the link suggests.
func ParseTelegramLink(uri string) (TelegramLink, bool) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return TelegramLink{}, false
	}
	switch {
	case hasFoldedPrefix(uri, "tg:"):
		return parseTGScheme(uri[len("tg:"):])
	case hasFoldedPrefix(uri, "https://t.me/"), hasFoldedPrefix(uri, "http://t.me/"),
		hasFoldedPrefix(uri, "https://telegram.me/"), hasFoldedPrefix(uri, "http://telegram.me/"):
		return parseTMe(uri[strings.Index(uri, "t.me/")+len("t.me/"):])
	}
	// Also allow a bare t.me/<name> form, since those are what people paste.
	if hasFoldedPrefix(uri, "t.me/") || hasFoldedPrefix(uri, "telegram.me/") {
		return parseTMe(uri[strings.Index(uri, ".me/")+len(".me/"):])
	}
	return TelegramLink{}, false
}

// parseTGScheme reads what follows "tg:" (double-slash and all).
func parseTGScheme(rest string) (TelegramLink, bool) {
	rest = strings.TrimPrefix(rest, "//")
	head, query, _ := strings.Cut(rest, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return TelegramLink{}, false
	}
	switch strings.ToLower(head) {
	case "resolve":
		if phone := values.Get("phone"); values.Get("domain") == "" && phone != "" {
			if !isPhoneDigits(phone) {
				return TelegramLink{}, false
			}
			return TelegramLink{Kind: TelegramLinkPhone, Phone: phone, Text: values.Get("text")}, true
		}
		name := strings.TrimPrefix(values.Get("domain"), "@")
		if !isTelegramUsername(name) {
			return TelegramLink{}, false
		}
		return TelegramLink{Kind: TelegramLinkUsername,
			Username: strings.ToLower(name), Post: postOf(values), Text: values.Get("text")}, true
	case "join":
		invite := values.Get("invite")
		if !isInviteHash(invite) {
			return TelegramLink{}, false
		}
		return TelegramLink{Kind: TelegramLinkInvite, Invite: invite}, true
	case "privatepost":
		channel, ok := parsePositiveInt64(values.Get("channel"))
		if !ok {
			return TelegramLink{}, false
		}
		return TelegramLink{Kind: TelegramLinkPrivateChannel,
			Channel: channel, Post: postOf(values)}, true
	}
	return TelegramLink{}, false
}

// parseTMe reads what follows "t.me/": "<name>[/<post>]", "c/<channel>/<post>", "s/<name>"
// (name's web preview), "+<phone>", "+<invite>" or "joinchat/<invite>". Everything else —
// addstickers, proxy, settings pages — is not a chat kith can jump to.
func parseTMe(rest string) (TelegramLink, bool) {
	rest, _, _ = strings.Cut(rest, "#")
	rest, query, _ := strings.Cut(rest, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		values = url.Values{} // the text is a suggestion; a bad query loses only it
	}
	rest = strings.TrimPrefix(rest, "s/") // s/<name> is the sharable preview, same chat
	first, tail, hasTail := strings.Cut(rest, "/")
	first, err = url.PathUnescape(first)
	if err != nil {
		return TelegramLink{}, false
	}
	if strings.EqualFold(first, "c") {
		return parsePrivateTMe(tail)
	}
	// t.me/+<digits> is a phone number, as the Telegram apps read it; any other
	// +<hash> is an invite.
	if plus, ok := strings.CutPrefix(first, "+"); ok && !hasTail {
		if isPhoneDigits(plus) {
			return TelegramLink{Kind: TelegramLinkPhone, Phone: plus, Text: values.Get("text")}, true
		}
		if isInviteHash(plus) {
			return TelegramLink{Kind: TelegramLinkInvite, Invite: plus}, true
		}
		return TelegramLink{}, false
	}
	if strings.EqualFold(first, "joinchat") {
		if !isInviteHash(tail) {
			return TelegramLink{}, false
		}
		return TelegramLink{Kind: TelegramLinkInvite, Invite: tail}, true
	}
	first = strings.TrimPrefix(first, "@")
	if reservedTMePath[strings.ToLower(first)] || !isTelegramUsername(first) {
		return TelegramLink{}, false
	}
	post := 0
	if hasTail && tail != "" {
		p, ok := parsePost(tail)
		if !ok {
			return TelegramLink{}, false
		}
		post = p
	}
	return TelegramLink{Kind: TelegramLinkUsername,
		Username: strings.ToLower(first), Post: post, Text: values.Get("text")}, true
}

// reservedTMePath are t.me first-segments that are not public chats: sticker sets and
// settings pages the Telegram apps serve themselves.
var reservedTMePath = map[string]bool{
	"addstickers": true, "addemoji": true,
	"proxy": true, "socks": true, "login": true, "setlanguage": true,
	"share": true, "iv": true, "bg": true, "contact": true,
}

// parsePrivateTMe reads what follows "t.me/c/": "<channel>[/<post>]". The channel is a
// positive integer (the -100 mark is implicit in private links).
func parsePrivateTMe(rest string) (TelegramLink, bool) {
	if rest == "" {
		return TelegramLink{}, false
	}
	first, tail, hasTail := strings.Cut(rest, "/")
	channel, ok := parsePositiveInt64(first)
	if !ok {
		return TelegramLink{}, false
	}
	post := 0
	if hasTail {
		if post, ok = parsePost(tail); !ok {
			return TelegramLink{}, false
		}
	}
	return TelegramLink{Kind: TelegramLinkPrivateChannel,
		Channel: channel, Post: post}, true
}

// postOf reads the "post" or "message_id" query value; 0 when absent.
func postOf(values url.Values) int {
	for _, key := range []string{"post", "message_id"} {
		if raw := values.Get(key); raw != "" {
			if post, ok := parsePost(raw); ok {
				return post
			}
		}
	}
	return 0
}

// parsePost accepts "<n>" or "<n>/<thread>" (a topic's reply), taking only the message
// number. 0 or negative is rejected.
func parsePost(raw string) (int, bool) {
	raw, _, _ = strings.Cut(raw, "/")
	raw, _, _ = strings.Cut(raw, "?")
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parsePositiveInt64 reads a positive int64; 0 or negative is rejected.
func parsePositiveInt64(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// isTelegramUsername is Telegram's rule: 5–32 chars, letters/digits/underscore, must
// start with a letter. kith is permissive on length — Telegram grandfathers shorter
// legacy names — but keeps the character set so a stray "+" or "/" is caught.
func isTelegramUsername(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isPhoneDigits is a phone number as Telegram links write it: digits only, no "+", at
// most E.164's 15.
func isPhoneDigits(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isInviteHash is an invite hash's character set (URL-safe base64), so a stray "/" or
// "?" is caught.
func isInviteHash(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
