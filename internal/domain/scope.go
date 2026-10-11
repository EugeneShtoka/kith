package domain

import "strings"

// The shared specificity ladder that decides which rule wins where.

// Scope is where something happens, in terms a rule can name.
type Scope struct {
	// RoomID, not a displayed name, is what rules match.
	RoomID string
	// Space is the room's first home: a space's name, or tag:<name>.
	Space string
	// SpaceLabel is that home's rail label when renamed, else "".
	SpaceLabel string
	// Thread is the root event, empty for the main timeline.
	Thread string
	// Sender is the speaker's MXID, empty for a question about a place.
	Sender string
}

// Rank is how narrowly a rule names a place; higher is narrower and wins.
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

// Match is one rule's constraints. Empty fields mean anywhere/anyone, but a rule with
// both empty matches nothing.
type Match struct {
	// Place is a room ID, a home entry (NamesHome), or a thread's root event.
	Place string
	// Sender is one person's MXID.
	Sender string
}

// Rank scores how specifically m names scope, or NoMatch.
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
	place := strings.TrimSpace(m.Place)
	switch {
	case scope.Thread != "" && place == scope.Thread:
		if named {
			return RankThreadSender
		}
		return RankThread
	case scope.RoomID != "" && place == scope.RoomID:
		if named {
			return RankRoomSender
		}
		return RankRoom
	case scope.Space != "" && NamesHome(place, scope.Space, scope.SpaceLabel):
		if named {
			return RankSpaceSender
		}
		return RankSpace
	default:
		return NoMatch
	}
}

// Narrowest returns the index of the narrowest matching rule; on a tie the later wins.
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

// ThreadCap is how many threads to list under one room: the narrowest rule's Max, else
// base; negative means no cap (0), zero means fallback.
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
