package config

// Telegram is [telegram]: kith's own link to Telegram, as a client of its own, with no
// Matrix bridge. It runs when it has an account.
type Telegram struct {
	Accounts []TelegramAccount `toml:"account"`
}

// TelegramAccount is one [[telegram.account]]: a phone number kith logs in as, under a
// name `kith login telegram <name>` takes. The account's own user ID, which every room
// and message ID carries, comes from the login.
type TelegramAccount struct {
	Name  string `toml:"name"`
	Phone string `toml:"phone"` // international, "+" and separators allowed
}
