package domain

import (
	"net/url"
	"strconv"
	"strings"
)

// Telegram addresses (tg: URIs, with t.me's web form kept for completeness) parsed into
// chats a Telegram adapter can look up: a public chat by its @username, or a private
// channel by its ID.

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
)

// TelegramLink is one Telegram address: what it names, and the message in it if the
// link named one.
type TelegramLink struct {
	Kind TelegramLinkKind
	// Username is the @name (lowercased, without "@"), for a public chat.
	Username string
	// Channel is the raw channel ID (without the -100 mark), for a private channel.
	Channel int64
	// Post is the message ID in the chat, 0 when the link named only the chat.
	Post int
}

// ParseTelegramLink reads one URI as a Telegram address, and reports whether it is one.
// It accepts the two tg: forms the Telegram apps write — tg://resolve?domain=X[&post=N]
// and tg://privatepost?channel=X&post=N — and their https://t.me equivalents. Join
// links (tg://join, t.me/+<invite>) are rejected: joining a channel kith is not in is
// a feature of its own, not a link follow.
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
		name := strings.TrimPrefix(values.Get("domain"), "@")
		if !isTelegramUsername(name) {
			return TelegramLink{}, false
		}
		return TelegramLink{Kind: TelegramLinkUsername,
			Username: strings.ToLower(name), Post: postOf(values)}, true
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
// (name's web preview). Everything else — joinchat, +invite, addstickers, proxy — is
// not a chat kith can jump to.
func parseTMe(rest string) (TelegramLink, bool) {
	rest, _, _ = strings.Cut(rest, "?")
	rest, _, _ = strings.Cut(rest, "#")
	rest = strings.TrimPrefix(rest, "s/") // s/<name> is the sharable preview, same chat
	first, tail, hasTail := strings.Cut(rest, "/")
	first, err := url.PathUnescape(first)
	if err != nil {
		return TelegramLink{}, false
	}
	if strings.EqualFold(first, "c") {
		return parsePrivateTMe(tail)
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
		Username: strings.ToLower(first), Post: post}, true
}

// reservedTMePath are t.me first-segments that are not public chats: invite flows and
// settings pages the Telegram apps serve themselves.
var reservedTMePath = map[string]bool{
	"joinchat": true, "addstickers": true, "addemoji": true,
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
