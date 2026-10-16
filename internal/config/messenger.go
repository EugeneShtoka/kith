package config

// Messenger is [messenger]: kith's direct Messenger connection (no bridge);
// active when it has an account.
type Messenger struct {
	Accounts []MessengerAccount `toml:"account"`
}

// MessengerAccount is one [[messenger.account]], named for `kith login messenger
// <name>`. The Meta account it is comes from the login.
type MessengerAccount struct {
	Name string `toml:"name"`
}
