package domain

// PlaceFilter is an include/exclude list of places.
type PlaceFilter struct {
	Include, Exclude []string
}

// Admits reports whether room is in scope. Entries are RoomFacts entries: a room ID
// or name, `space:Work`, `protocol:WhatsApp`, `dm`, `group`.
func (f PlaceFilter) Admits(room RoomFacts) bool {
	if namesAny(f.Exclude, room) {
		return false
	}
	return len(f.Include) == 0 || namesAny(f.Include, room)
}

// Everywhere reports a filter with no includes; some callers treat that as a refusal
// (see CodeScope).
func (f PlaceFilter) Everywhere() bool { return len(f.Include) == 0 }
