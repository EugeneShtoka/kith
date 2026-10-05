package config

// WhatsApp is [whatsapp]: kith's own link to WhatsApp, as a linked device, beside or
// instead of a Matrix bridge. It runs when it has an account.
type WhatsApp struct {
	Accounts []WhatsAppAccount `toml:"account"`
	Archive  NetworkArchive    `toml:"archive"`
}

// WhatsAppAccount is one [[whatsapp.account]]: a phone number kith links to, under a
// name `kith login whatsapp <name>` takes.
type WhatsAppAccount struct {
	Name  string `toml:"name"`
	Phone string `toml:"phone"` // international, "+" and separators allowed
}
