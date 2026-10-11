package domain

// Activity is what other people in a room are doing now.
type Activity struct {
	RoomID RoomID
	// Typing is everyone typing in the room, ourselves excluded.
	Typing []string
}
