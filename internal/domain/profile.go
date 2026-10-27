package domain

// Profile is a named setup a client runs: the rooms it shows. A room outside Rooms
// shows nothing and counts for nothing in it.
type Profile struct {
	Name  string
	Rooms PlaceFilter
	// Tags are the tags as this profile has them, where its archive differs from the
	// shared one; nil runs the shared tags.
	Tags *TagSet
}

// Holds reports whether the profile holds a room: its rooms, judging tag: entries
// with the profile's own tags when it has them.
func (p Profile) Holds(facts RoomFacts) bool {
	if p.Tags != nil {
		facts.Tags = p.Tags.Of(facts)
	}
	return p.Rooms.Admits(facts)
}

// Everyone reports whether the profile holds every room, whatever its tags say.
func (p Profile) Everyone() bool { return len(p.Rooms.Include) == 0 && len(p.Rooms.Exclude) == 0 }
