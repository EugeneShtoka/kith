package config

// Matrix is [matrix]: the Matrix accounts kith runs, each on its homeserver.
type Matrix struct {
	Accounts []MatrixAccount `toml:"account"`
	// Default names the account a call naming none goes to when several could.
	Default string `toml:"default"`
}

// MatrixAccount is one [[matrix.account]], named for `kith login matrix <name>`.
type MatrixAccount struct {
	Name       string `toml:"name"`
	Homeserver string `toml:"homeserver"` // the client-server API's base URL
	User       string `toml:"user"`       // the full Matrix ID, @you:example.org
}

// Network is "Matrix".
func (Matrix) Network() string { return "Matrix" }

// Table is "matrix.account".
func (Matrix) Table() string { return "matrix.account" }

// AccountNames is each [[matrix.account]]'s name.
func (n Matrix) AccountNames() []string {
	return accountNames(n.Accounts, func(a MatrixAccount) string { return a.Name })
}

// DefaultAccount is [matrix] default.
func (n Matrix) DefaultAccount() string { return n.Default }
