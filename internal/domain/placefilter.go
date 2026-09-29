package domain

// Include/exclude over places — the filter shape this client uses wherever the question
// is "does this apply here" rather than "which rule wins".
type PlaceFilter struct {
	Include, Exclude []string
}

// Admits reports whether the place is in scope, with each entry read as a RoomFacts
// entry: a room's ID or name bare, `space:Work`, `protocol:WhatsApp`, `dm`, `group`.
func (f PlaceFilter) Admits(room RoomFacts) bool {
	if namesAny(f.Exclude, room) {
		return false
	}
	return len(f.Include) == 0 || namesAny(f.Include, room)
}

// Everywhere reports a filter that names no place, which for some callers is a
// meaningful refusal rather than a permissive default — see CodeScope.
func (f PlaceFilter) Everywhere() bool { return len(f.Include) == 0 }
