package domain

// Profile is a named setup a client runs: the rooms it shows. A room outside Rooms
// shows nothing and counts for nothing in it.
type Profile struct {
	Name  string
	Rooms PlaceFilter
}
