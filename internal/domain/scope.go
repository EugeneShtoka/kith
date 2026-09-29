package domain

// One scope, everywhere — the ladder that decides which rule wins where.

// Scope is where something is happening, in the terms a rule can name: the room, the
// space it belongs to, the thread it is in, and who is speaking.
type Scope struct {
	// RoomID is what a rule matches on; a displayed name is not a handle.
	RoomID string
	// Space is the space the room is in.
	Space string
	// Thread is the root event, empty for the main timeline.
	Thread string
	// Sender is the speaker's MXID, empty for a question about a place.
	Sender string
}

// Rank is how narrowly a rule names a place, and therefore which rule wins. Higher is
// narrower.
type Rank int

// The rungs, widest first.
const (
	NoMatch Rank = iota
	RankSpace
	RankSender
	RankRoom
	RankThread
	RankSpaceSender
	RankRoomSender
	RankThreadSender
)

// Match is the constraints one rule carries. Empty fields mean "anywhere" and "anyone",
// so a rule with neither names nothing and matches nothing.
type Match struct {
	// Place is a room ID, a space's name, or a thread's root event.
	Place string
	// Sender is one person's MXID.
	Sender string
}

// Rank scores how specifically this match names a scope, or NoMatch when it does not
// name it at all.
func (m Match) Rank(scope Scope) Rank {
	if m.Sender != "" && m.Sender != scope.Sender {
		return NoMatch
	}
	named := m.Sender != ""

	if m.Place == "" {
		if !named {
			return NoMatch
		}
		return RankSender // constrained only by who, so: this person anywhere
	}
	switch m.Place {
	case scope.Thread:
		if scope.Thread == "" {
			return NoMatch
		}
		if named {
			return RankThreadSender
		}
		return RankThread
	case scope.RoomID:
		if named {
			return RankRoomSender
		}
		return RankRoom
	case scope.Space:
		if named {
			return RankSpaceSender
		}
		return RankSpace
	default:
		return NoMatch
	}
}

// Narrowest returns the index of the rule that applies, and false when none does.
func Narrowest(matches []Match, scope Scope) (int, bool) {
	best, bestRank := -1, NoMatch
	for i, match := range matches {
		if rank := match.Rank(scope); rank >= bestRank && rank > NoMatch {
			best, bestRank = i, rank
		}
	}
	return best, best >= 0
}

// Matches maps a rule list to the matches Narrowest reads.
func Matches[T any](rules []T, of func(T) Match) []Match {
	out := make([]Match, len(rules))
	for i, rule := range rules {
		out[i] = of(rule)
	}
	return out
}

// ThreadRule caps one place's thread rows differently from the rest.
type ThreadRule struct {
	Match string
	Max   int
}

// ThreadCap is how many threads to list under one room: the narrowest rule naming it,
// or the number that applies everywhere else.
func ThreadCap(base, fallback int, rules []ThreadRule, scope Scope) int {
	at, ok := Narrowest(Matches(rules, func(r ThreadRule) Match {
		return Match{Place: r.Match}
	}), scope)
	if ok {
		base = rules[at].Max
	}
	switch {
	case base < 0:
		return 0 // no cap: list them all
	case base == 0:
		return fallback
	default:
		return base
	}
}
