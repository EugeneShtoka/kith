package domain

// PublicRoom is one row of the Matrix public rooms directory, as the homeserver
// serves it. It is not a cached Room — a room joined through the directory
// becomes a Room on the next sync; this type carries only what the chooser
// shows and what joining needs.
type PublicRoom struct {
	// ID is the room ID the directory listed; always set, even when Alias is empty.
	ID RoomID
	// Alias is the room's canonical alias, when set (e.g. #kith:matrix.org).
	Alias string
	// Name is the room's displayed name; empty for a nameless room.
	Name string
	// Topic is the public room topic; empty when none is set.
	Topic string
	// Members is num_joined_members (homeserver estimate, not real-time).
	Members int
	// WorldReadable reports whether a non-member can read the room.
	WorldReadable bool
	// Encrypted reports whether the room runs E2EE (an m.room.encryption state event).
	Encrypted bool
}

// PublicRoomsPage is one page of the directory, with the cursor into the next page.
// NextSince is empty when the server has no more rows.
type PublicRoomsPage struct {
	Rooms     []PublicRoom
	NextSince string
	// TotalEstimate is the homeserver's estimate of how many rooms match in total;
	// absent (zero) when the server did not say.
	TotalEstimate int
}
