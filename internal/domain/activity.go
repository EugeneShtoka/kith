package domain

// Activity is what the *other* people in a room are doing right now: who is typing into
// it.
type Activity struct {
	RoomID RoomID
	// Typing is everyone currently typing in the room, ourselves excluded: we know what
	// we are doing, and a client that told you about your own typing would be reporting
	// your keystrokes back to you.
	Typing []string
}
