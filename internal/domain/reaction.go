package domain

// Reaction is a single m.reaction annotation on a target message.
type Reaction struct {
	ID     EventID // the m.reaction event's own ID
	RoomID RoomID
	Target EventID // the message this annotates
	Sender string
	Key    string // the reaction key (typically an emoji)
}

// ReactionUpdate is one live reaction change: added, or Removed by redaction.
type ReactionUpdate struct {
	Reaction Reaction
	Removed  bool
}

// ReactionTally is one key's aggregate under a message; Mine marks the user's own.
type ReactionTally struct {
	Key   string
	Count int
	Mine  bool
}

// AggregateReactions tallies reactions by key in order of first appearance, so the
// row is stable across renders.
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
