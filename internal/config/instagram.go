package config

// Instagram is [instagram]: kith's direct Instagram DMs connection (no bridge), signed
// in with an instagram.com session's cookies; active when it has an account.
type Instagram struct {
	Accounts []InstagramAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string `toml:"default"`
}

// InstagramAccount is one [[instagram.account]], named for `kith login instagram
// <name>`. Which Instagram account it is comes from signing in.
type InstagramAccount struct {
	Name string `toml:"name"`
}
