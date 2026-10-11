package config

// Telegram is [telegram]: kith's direct Telegram client (no bridge); active when it has
// an account.
type Telegram struct {
	Accounts []TelegramAccount `toml:"account"`
	Archive  NetworkArchive    `toml:"archive"`
}

// TelegramAccount is one [[telegram.account]], named for `kith login telegram <name>`.
// The user ID in room IDs comes from the login.
type TelegramAccount struct {
	Name  string `toml:"name"`
	Phone string `toml:"phone"` // international, "+" and separators allowed
}
