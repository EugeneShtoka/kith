package domain

// Reaction is a single m.reaction annotation: someone reacted to a target message with
// a key (an emoji or short string).
type Reaction struct {
	ID     EventID // the m.reaction event's own ID
	RoomID RoomID
	Target EventID // the message this annotates
	Sender string
	Key    string // the reaction key (typically an emoji)
}

// ReactionUpdate is one live reaction change streamed from the sync loop: an added
// reaction, or — when Removed — one taken back via redaction.
type ReactionUpdate struct {
	Reaction Reaction
	Removed  bool
}

// ReactionTally is the per-key aggregate rendered beneath a message: the key, how many
// people reacted with it, and whether the current user is among them (so the UI can
// mark the ones they can toggle off).
type ReactionTally struct {
	Key   string
	Count int
	Mine  bool
}

// AggregateReactions groups a target's reactions by key into display tallies, ordered
// by first appearance so the row is stable across renders.
func AggregateReactions(rs []Reaction, me string) []ReactionTally {
	idx := make(map[string]int, len(rs))
	seen := make(map[EventID]bool, len(rs))
	tallies := make([]ReactionTally, 0)
	for _, r := range rs {
		if r.Key == "" || (r.ID != "" && seen[r.ID]) {
			continue
		}
		if r.ID != "" {
			seen[r.ID] = true
		}
		i, ok := idx[r.Key]
		if !ok {
			i = len(tallies)
			idx[r.Key] = i
			tallies = append(tallies, ReactionTally{Key: r.Key})
		}
		tallies[i].Count++
		if me != "" && r.Sender == me {
			tallies[i].Mine = true
		}
	}
	return tallies
}
