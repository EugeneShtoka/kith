package domain

import (
	"slices"
	"strings"
)

// CardIdentifiers is what a phonebook card knows a person by, from all the IDs kith has
// of them: each written as the card writes it, by key (phone, matrix, telegram,
// whatsapp, slack). A number is a phone, however the network spelled it; a bridge's
// puppet is the network's user where kith can tell it.
func CardIdentifiers(ids []string) map[string][]string {
	out := map[string][]string{}
	add := func(key, value string) {
		if value != "" && !slices.Contains(out[key], value) {
			out[key] = append(out[key], value)
		}
	}
	for _, x := range ids {
		if digits := PhoneOf(x); digits != "" {
			add("phone", "+"+digits)
			continue
		}
		if digits, ok := strings.CutPrefix(x, phonePrefix); ok {
			add("phone", "+"+digits)
			continue
		}
		id := ParseID(x)
		switch {
		case id.Network == ProtocolTelegram, id.Network == ProtocolWhatsApp, id.Network == ProtocolSlack:
			add(strings.ToLower(prefixOf(id.Network)), id.Native)
		case !IsMatrixUserID(x):
		case ProtocolOf(x) == ProtocolMatrix:
			add("matrix", x)
		default:
			if user, ok := telegramPuppet(x); ok {
				add("telegram", user)
			}
		}
	}
	return out
}

// telegramPuppet is the Telegram user a bridge's puppet (@telegram_123:server) is.
func telegramPuppet(mxid string) (string, bool) {
	local, _, _ := strings.Cut(strings.TrimPrefix(mxid, "@"), ":")
	user, ok := strings.CutPrefix(local, "telegram_")
	if !ok || user == "" || strings.Trim(user, "0123456789") != "" {
		return "", false
	}
	return user, true
}
