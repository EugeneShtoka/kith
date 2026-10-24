package config

// WhatsApp is [whatsapp]: kith's direct WhatsApp connection as a linked device; active
// when it has an account.
type WhatsApp struct {
	Accounts []WhatsAppAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string         `toml:"default"`
	Archive NetworkArchive `toml:"archive"`
}

// WhatsAppAccount is one [[whatsapp.account]], named for `kith login whatsapp <name>`.
type WhatsAppAccount struct {
	Name  string `toml:"name"`
	Phone string `toml:"phone"` // international, "+" and separators allowed
}
