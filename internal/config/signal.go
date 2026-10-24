package config

// Signal is [signal]: kith's direct Signal connection (no bridge), linked to the phone
// as a secondary device, as Signal Desktop is; active when it has an account.
type Signal struct {
	Accounts []SignalAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string `toml:"default"`
}

// SignalAccount is one [[signal.account]], named for `kith login signal <name>`.
// Which Signal account it is comes from linking.
type SignalAccount struct {
	Name string `toml:"name"`
}
