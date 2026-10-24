package config

// LinkedIn is [linkedin]: kith's direct LinkedIn connection (no bridge); active when
// it has an account.
type LinkedIn struct {
	Accounts []LinkedInAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string `toml:"default"`
}

// LinkedInAccount is one [[linkedin.account]], named for `kith login linkedin <name>`.
// The LinkedIn account it is comes from the login.
type LinkedInAccount struct {
	Name string `toml:"name"`
}
