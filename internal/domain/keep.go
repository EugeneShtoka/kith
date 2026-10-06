package domain

// KeepRule is how many messages one place keeps, overriding the setting everywhere.
type KeepRule struct {
	// Match is a place entry (ParseEntry): a room, a space, a tag, a network.
	Match    string
	Messages int // negative keeps every one
}

// MessagesKept is how many messages room keeps: the narrowest rule naming it (one
// room over a set; among equals, the last), else base. Negative keeps every one.
func MessagesKept(base int, rules []KeepRule, room RoomFacts) int {
	kept, best := base, EntryInvalid
	for _, r := range rules {
		if kind, ok := room.Match(r.Match); ok && kind >= best {
			kept, best = r.Messages, kind
		}
	}
	return kept
}
