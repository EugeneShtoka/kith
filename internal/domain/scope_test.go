package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The ladder every per-place setting in this client is resolved with, stated once and
// tested once: more constraints beat fewer, and among equals the narrower place wins.

var atWork = domain.Scope{
	RoomID: "!room:example.org",
	Space:  "Work",
	Thread: "$thread",
	Sender: "@dana:example.org",
}

func TestRankOrdersTheLadder(t *testing.T) {
	t.Parallel()

	// Written widest to narrowest.
	ordered := []struct {
		name  string
		match domain.Match
	}{
		{"the space", domain.Match{Place: "Work"}},
		{"this person anywhere", domain.Match{Sender: "@dana:example.org"}},
		{"the room", domain.Match{Place: "!room:example.org"}},
		{"the thread", domain.Match{Place: "$thread"}},
		{"this person in the space", domain.Match{Place: "Work", Sender: "@dana:example.org"}},
		{"this person in the room", domain.Match{Place: "!room:example.org", Sender: "@dana:example.org"}},
		{"this person in the thread", domain.Match{Place: "$thread", Sender: "@dana:example.org"}},
	}
	for i := 1; i < len(ordered); i++ {
		lower, higher := ordered[i-1], ordered[i]
		if higher.match.Rank(atWork) <= lower.match.Rank(atWork) {
			t.Errorf("%q does not outrank %q", higher.name, lower.name)
		}
	}
}

// A rule naming nothing matches nothing.
func TestARuleNamingNothingMatchesNothing(t *testing.T) {
	t.Parallel()

	if got := (domain.Match{}).Rank(atWork); got != domain.NoMatch {
		t.Errorf("an empty match ranked %v, want no match", got)
	}
}

func TestARuleNamingSomewhereElseDoesNotMatch(t *testing.T) {
	t.Parallel()

	for name, match := range map[string]domain.Match{
		"another room":   {Place: "!elsewhere:example.org"},
		"another space":  {Place: "Home"},
		"another person": {Sender: "@someone:example.org"},
		"this room, but somebody else": {
			Place: "!room:example.org", Sender: "@someone:example.org",
		},
	} {
		if got := match.Rank(atWork); got != domain.NoMatch {
			t.Errorf("%s ranked %v, want no match", name, got)
		}
	}
}

// A thread rung only exists where there is a thread. On the main timeline, a rule
// naming a thread names somewhere else.
func TestTheThreadRungNeedsAThread(t *testing.T) {
	t.Parallel()

	main := domain.Scope{RoomID: atWork.RoomID, Space: atWork.Space, Sender: atWork.Sender}
	if got := (domain.Match{Place: "$thread"}).Rank(main); got != domain.NoMatch {
		t.Errorf("a thread rule ranked %v in the main timeline, want no match", got)
	}
	if got := (domain.Match{Place: "$thread"}).Rank(atWork); got != domain.RankThread {
		t.Errorf("a thread rule ranked %v in its own thread", got)
	}
}

// Among rules of equal specificity the last one wins.
func TestAmongEqualsTheLastOneWins(t *testing.T) {
	t.Parallel()

	matches := []domain.Match{
		{Place: "!room:example.org"},
		{Place: "Work"},
		{Place: "!room:example.org"},
	}
	at, ok := domain.Narrowest(matches, atWork)
	if !ok || at != 2 {
		t.Errorf("Narrowest = %d, %v; want the later of the two room rules", at, ok)
	}
}

func TestNarrowestFindsNothingWhenNothingNamesIt(t *testing.T) {
	t.Parallel()

	if _, ok := domain.Narrowest([]domain.Match{{Place: "Home"}}, atWork); ok {
		t.Error("a rule about somewhere else was chosen")
	}
	if _, ok := domain.Narrowest(nil, atWork); ok {
		t.Error("an empty rule list chose something")
	}
}

// The ladder's first new customer: one room listing more threads than the rest.
func TestThreadCapResolvesPerPlace(t *testing.T) {
	t.Parallel()

	rules := []domain.ThreadRule{
		{Match: "Work", Max: 8},
		{Match: "!room:example.org", Max: 12},
		{Match: "!quiet:example.org", Max: -1},
	}
	busy := domain.Scope{RoomID: "!room:example.org", Space: "Work"}
	if got := domain.ThreadCap(3, 5, rules, busy); got != 12 {
		t.Errorf("the busy room capped at %d, want its own rule (12) over its space's", got)
	}
	other := domain.Scope{RoomID: "!other:example.org", Space: "Work"}
	if got := domain.ThreadCap(3, 5, rules, other); got != 8 {
		t.Errorf("a room in Work capped at %d, want the space's rule (8)", got)
	}
	elsewhere := domain.Scope{RoomID: "!far:example.org", Space: "Home"}
	if got := domain.ThreadCap(3, 5, rules, elsewhere); got != 3 {
		t.Errorf("a room nothing names capped at %d, want the global number", got)
	}
	// Zero means "the default" and negative means "no cap", at both levels — a rule
	// that read the same number differently would be a second thing to learn.
	quiet := domain.Scope{RoomID: "!quiet:example.org"}
	if got := domain.ThreadCap(3, 5, rules, quiet); got != 0 {
		t.Errorf("a room with no cap resolved to %d, want 0 (list them all)", got)
	}
	if got := domain.ThreadCap(0, 5, nil, elsewhere); got != 5 {
		t.Errorf("an unset global resolved to %d, want the default", got)
	}
}
