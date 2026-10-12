package domain

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// A filter catches messages in the places it watches; its moves decide whether a catch
// moves the room, with the ratio counted by the filter's own numbers.
func TestFiltersCatchAndMove(t *testing.T) {
	t.Parallel()

	crypto := Filter{Name: "crypto", Tag: "Spam", Words: Tracked{Words: []string{"*bitcoin*", "invest*"}},
		Moves: FilterMoves{FirstMessage: true, Direct: true, Ratio: true}, Ratio: 0.6, Floor: 5, Window: time.Hour}
	number := Filter{Name: "that number", Tag: "Spam", Words: Tracked{Words: []string{"*"}}, From: "@spam:x",
		Moves: FilterMoves{FirstMessage: true, Direct: true}}
	elections := Filter{Name: "elections", Tag: "Elections", Words: Tracked{Words: []string{"vote"}},
		In: []string{"place:SMS"}, Moves: FilterMoves{Always: true}}
	filters := Filters{crypto, number, elections}
	sms := RoomFacts{ID: "!s:x", Spaces: []string{"Google Messages"}, SpaceLabels: []string{"SMS"}}
	work := RoomFacts{ID: "!w:x", Spaces: []string{"Work"}}

	names := func(fs []Filter) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	for _, tc := range []struct {
		body, sender string
		room         RoomFacts
		want         []string
	}{
		{"morning all", "@dana:x", work, nil},
		{"double your Bitcoin", "@dana:x", work, []string{"crypto"}},
		{"hello", "@dana:x", work, nil}, // the sender's filter, somebody else
		{"hello", "@spam:x", work, []string{"that number"}},
		{"go vote", "@dana:x", work, nil}, // not a place it watches
		{"go vote", "@dana:x", sms, []string{"elections"}},
		{"vote bitcoin", "@spam:x", sms, []string{"crypto", "that number", "elections"}},
	} {
		if got := names(filters.Catching(tc.body, tc.sender, tc.room)); !slices.Equal(got, tc.want) {
			t.Errorf("Catching(%q, %s, %s) = %v, want %v", tc.body, tc.sender, tc.room.ID, got, tc.want)
		}
	}

	byRatio := Filter{Name: "share", Moves: FilterMoves{Ratio: true}, Ratio: 0.6, Floor: 5}
	for name, tc := range map[string]struct {
		filter Filter
		c      MoveCase
		want   MoveRule
	}{
		"always moves on any catch":       {elections, MoveCase{}, MoveAlways},
		"the first message":               {crypto, MoveCase{First: true}, MoveFirstMessage},
		"a direct message is its sender":  {crypto, MoveCase{Direct: true}, MoveDirect},
		"not one you wrote in":            {crypto, MoveCase{Direct: true, Mine: true}, MoveNone},
		"a group needs the share":         {crypto, MoveCase{}, MoveNone},
		"most of them":                    {crypto, MoveCase{Caught: 7, Total: 10}, MoveMostly},
		"under the floor is not evidence": {crypto, MoveCase{Caught: 3, Total: 3}, MoveNone},
		"under the share":                 {crypto, MoveCase{Caught: 3, Total: 10}, MoveNone},
		"no ratio move, no ratio":         {number, MoveCase{Caught: 7, Total: 10}, MoveNone},
		"no first-message move":           {byRatio, MoveCase{First: true}, MoveNone},
		"no direct move":                  {byRatio, MoveCase{Direct: true}, MoveNone},
	} {
		if got, moved := tc.filter.Moving(tc.c); got != tc.want || moved != (tc.want != MoveNone) {
			t.Errorf("%s: Moving(%+v) = %v %t, want %v", name, tc.c, got, moved, tc.want)
		}
	}
}

// Every rule has a reason naming the filter; a release is never in its tag.
func TestEveryMoveHasAReason(t *testing.T) {
	t.Parallel()
	for _, rule := range []MoveRule{MoveAlways, MoveFirstMessage, MoveDirect, MoveMostly} {
		if reason := rule.Reason("crypto"); reason == "" || reason == rule.Reason("") {
			t.Errorf("rule %d: reason %q does not name the filter", rule, reason)
		}
	}
	if MoveNone.Reason("") != "" {
		t.Error("no move has a reason")
	}
	if (TagVerdict{Rule: MoveAlways, Released: true}).In() || !(TagVerdict{Rule: MoveAlways}).In() {
		t.Error("In() does not follow the release")
	}
}

// Over random sequences of moves, releases and clears, in any case spelling: a room's
// set keeps at most one entry per tag, only moves and releases, and a move never
// replaces a release; a retag keeps every other tag's entry and the release with it.
func TestVerdictSetsOverRandomSequences(t *testing.T) {
	t.Parallel()
	tags := []string{"Spam", "spam", "SPAM", "Leads", "leads", "Junk"}
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 5)) // #nosec G404 -- reproducible
		var set []TagVerdict
		released := map[string]bool{}
		for range 12 {
			tag := tags[rng.IntN(len(tags))]
			v := TagVerdict{Room: "!r:x", Tag: tag}
			switch rng.IntN(3) {
			case 0:
				v.Rule = MoveRule(1 + rng.IntN(4))
			case 1:
				v.Released = true
			}
			next, err := WithVerdict(set, v)
			key := strings.ToLower(tag)
			switch {
			case v.In() && released[key]:
				if !errors.Is(err, ErrReleased) || !slices.Equal(next, set) {
					t.Fatalf("seed %d: a move over a release = %v, %v", seed, next, err)
				}
			case err != nil:
				t.Fatalf("seed %d: %v", seed, err)
			default:
				set = next
				released[key] = v.Released
			}
			seen := map[string]bool{}
			for _, e := range set {
				k := strings.ToLower(e.Tag)
				if seen[k] || (!e.In() && !e.Released) || e.Released != released[k] {
					t.Fatalf("seed %d: set %+v breaks an invariant at %s", seed, set, e.Tag)
				}
				seen[k] = true
			}
		}
		before := len(set)
		retagged, _ := Retagged(set, "spam", "Junk") // Junk may hold an entry already: a combine
		dropped, _ := Retagged(set, "LEADS", "")
		if len(retagged) > before || len(dropped) > before {
			t.Fatalf("seed %d: retag changed the count: %d → %d / %d", seed, before, len(retagged), len(dropped))
		}
		combined := map[string]bool{}
		for _, e := range retagged {
			k := strings.ToLower(e.Tag)
			if k == "spam" || combined[k] {
				t.Fatalf("seed %d: after retagging spam into Junk: %+v", seed, retagged)
			}
			combined[k] = true
			if k == "junk" && e.Released != (released["spam"] || released["junk"]) {
				t.Fatalf("seed %d: the combined entry %+v lost a release (spam %t, junk %t)", seed, e, released["spam"], released["junk"])
			}
		}
		for _, e := range dropped {
			if SameTag(e.Tag, "leads") {
				t.Fatalf("seed %d: leads left after dropping it", seed)
			}
		}
	}
}
