package domain

// AccountPhase is where one network account is: what a client shows beside it.
type AccountPhase int

const (
	// AccountLoggedOut has no session (never logged in, rejected, or unlinked).
	AccountLoggedOut AccountPhase = iota + 1
	// AccountConnecting has a session and is reaching its network.
	AccountConnecting
	// AccountOnline has synced: its rooms in the cache are current.
	AccountOnline
	// AccountFailed stopped with an error.
	AccountFailed
)

// AccountStatus is one network account's state, as its adapter reports it.
type AccountStatus struct {
	Network Protocol
	// Account is what the config calls it: an MXID, or an account's name.
	Account string
	Phase   AccountPhase
	// Detail is why it is logged out or failed, or what it waits for.
	Detail string
}
