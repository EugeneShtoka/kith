package config

import "strings"

// WhatsApp is [whatsapp]: kith's own link to WhatsApp, as a linked device, beside or
// instead of a Matrix bridge. It runs when it has an account.
type WhatsApp struct {
	Accounts []WhatsAppAccount `toml:"account"`
}

// WhatsAppAccount is one [[whatsapp.account]]: a phone number kith links to, under a
// name `kith login whatsapp <name>` takes.
type WhatsAppAccount struct {
	Name  string `toml:"name"`
	Phone string `toml:"phone"` // international, "+" and separators allowed
}

// Digits is the phone number's digits alone: what WhatsApp pairs with, and the
// account's part of every room and message ID it sees.
func (a WhatsAppAccount) Digits() string {
	var b strings.Builder
	for _, r := range a.Phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
