package config

// Messenger is [messenger]: kith's direct Messenger connection (no bridge);
// active when it has an account.
type Messenger struct {
	Accounts []MessengerAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string `toml:"default"`
}

// MessengerAccount is one [[messenger.account]], named for `kith login messenger
// <name>`. The Meta account it is comes from the login.
type MessengerAccount struct {
	Name string `toml:"name"`
}
