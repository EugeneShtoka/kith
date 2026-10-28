package domain

// Change is what changed in the daemon, for a client to read again.
type Change int

const (
	// ChangeRooms is a network having rewritten its rooms.
	ChangeRooms Change = iota + 1
	// ChangeAccounts is a network account's state having changed.
	ChangeAccounts
)
