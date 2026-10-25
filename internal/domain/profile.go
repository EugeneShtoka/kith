package domain

// Profile is a named setup a client runs: the rooms it shows. A room outside Rooms
// shows nothing and counts for nothing in it.
type Profile struct {
	Name  string
	Rooms PlaceFilter
}

// DefaultProfile names the profile a config with none has: every room.
const DefaultProfile = "default"
