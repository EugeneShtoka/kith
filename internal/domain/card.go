package domain

import (
	"maps"
	"slices"
	"strings"
)

// CardIdentifiers maps a person's IDs to phonebook card keys (phone, matrix, telegram,
// whatsapp, slack). Any number becomes a phone; a recognizable bridge puppet becomes
// its network's user.
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
			add("matrix", MatrixNative(x))
		default:
			if user, ok := telegramPuppet(x); ok {
				add("telegram", user)
			}
		}
	}
	return out
}

// CardPeople is the IDs a card's identifier keys name, in kith's form, the other way
// from CardIdentifiers: a phone as its number's identifier (PhoneID), a Matrix,
// Telegram, WhatsApp or Slack ID as that network's person. An email names no one.
func CardPeople(ids map[string][]string) []string {
	networks := map[string]Protocol{"matrix": ProtocolMatrix, "telegram": ProtocolTelegram, "whatsapp": ProtocolWhatsApp, "slack": ProtocolSlack}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(ids)) {
		for _, value := range ids[key] {
			value = strings.TrimSpace(value)
			var id string
			if network, ok := networks[key]; ok && value != "" {
				id = NativePerson(network, value)
			} else if digits := PhoneDigits(value); key == "phone" && digits != "" {
				id = PhoneID(digits)
			}
			if id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// telegramPuppet is the Telegram user a bridge's puppet (@telegram_123:server) is.
func telegramPuppet(mxid string) (string, bool) {
	user, ok := strings.CutPrefix(Localpart(mxid), "telegram_")
	if !ok || user == "" || strings.Trim(user, "0123456789") != "" {
		return "", false
	}
	return user, true
}
