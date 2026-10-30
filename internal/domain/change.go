package domain

// Change is what changed in the daemon, for a client to read again.
type Change int

const (
	// ChangeRooms is a network having rewritten its rooms.
	ChangeRooms Change = iota + 1
	// ChangeAccounts is a network account's state having changed.
	ChangeAccounts
	// ChangeMessages is the messages a client holds being possibly stale: its message
	// stream could not resume, or the cache was cleared. The client's own stream says
	// so (read_again), never the daemon's Changed stream.
	ChangeMessages
	// ChangeRail is the rail of the client's profile having changed (Rail).
	ChangeRail
)
