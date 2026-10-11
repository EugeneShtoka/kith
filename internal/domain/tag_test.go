package domain

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func mustTags(t *testing.T, tags ...Tag) TagSet {
	t.Helper()
	set, warnings, err := NewTagSet(tags)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	return set
}

var (
	momDM   = RoomFacts{ID: "!mom:x", Name: "Mom", Direct: true, Protocol: ProtocolMatrix}
	workGrp = RoomFacts{ID: "whatsapp:44/1@g.us", Name: "Standup", Spaces: []string{"Work"}, Protocol: ProtocolWhatsApp}
	botsGrp = RoomFacts{ID: "!bots:x", Name: "Bots", Spaces: []string{"Work"}, Protocol: ProtocolMatrix}
)

// Some positive term and no `not` term must match (only `not` terms: no positive
// needed); an empty rule holds only picked rooms. Excluded beats picked beats the rule.
func TestATagHoldsWhatItsRuleSays(t *testing.T) {
	t.Parallel()
	set := mustTags(t,
		Tag{Name: "Family", Rule: []string{"dm", "room:Standup"}, Excluded: []string{"!mom:x"}},
		Tag{Name: "Work", Rule: []string{"space:Work", "not room:Bots"}, Picked: []string{"!bots:x"}},
		Tag{Name: "Everything", Rule: []string{"*"}},
		Tag{Name: "NotDMs", Rule: []string{"not dm"}},
		Tag{Name: "Pins"},
		Tag{Name: "Busy", Rule: []string{"unread", "mention", "not spam"}},
		Tag{Name: "WhatsApp", Rule: []string{"protocol:WhatsApp"}},
		Tag{Name: "Both", Picked: []string{"room:Bots"}, Excluded: []string{"!bots:x"}},
	)
	for _, tc := range []struct {
		tag   string
		facts RoomFacts
		state RoomState
		want  bool
	}{
		{"Family", momDM, RoomState{}, false}, // excluded beats the rule
		{"Family", workGrp, RoomState{}, true},
		{"Family", botsGrp, RoomState{}, false},
		{"Work", workGrp, RoomState{}, true},
		{"Work", botsGrp, RoomState{}, true}, // picked beats `not`
		{"Everything", botsGrp, RoomState{}, true},
		{"NotDMs", momDM, RoomState{}, false},
		{"NotDMs", workGrp, RoomState{}, true},
		{"Pins", workGrp, RoomState{}, false},
		{"Busy", workGrp, RoomState{Unread: true}, true},
		{"Busy", workGrp, RoomState{Mention: true}, true},
		{"Busy", workGrp, RoomState{Unread: true, Spam: true}, false},
		{"Busy", workGrp, RoomState{}, false},
		{"whatsapp", workGrp, RoomState{}, true}, // names are case-insensitive
		{"WhatsApp", botsGrp, RoomState{}, false},
		{"Nobody", workGrp, RoomState{}, false},
		{"Both", botsGrp, RoomState{}, false}, // picked and excluded: excluded wins
	} {
		if got := set.Has(tc.tag, tc.facts, tc.state); got != tc.want {
			t.Errorf("Has(%s, %s, %+v) = %t, want %t", tc.tag, tc.facts.Name, tc.state, got, tc.want)
		}
	}
}

// A rule may name other tags, positively or after `not`.
func TestARuleCanNameTags(t *testing.T) {
	t.Parallel()
	set := mustTags(t,
		Tag{Name: "Archived", Picked: []string{"!bots:x"}},
		Tag{Name: "Inbox", Rule: []string{"*", "not tag:Archived"}},
		Tag{Name: "WorkInbox", Rule: []string{"tag:Inbox", "not dm"}},
	)
	if set.Has("Inbox", botsGrp, RoomState{}) || !set.Has("Inbox", workGrp, RoomState{}) {
		t.Error("Inbox does not leave out what Archived holds")
	}
	if !set.Has("WorkInbox", workGrp, RoomState{}) || set.Has("WorkInbox", botsGrp, RoomState{}) {
		t.Error("a positive tag: term is not followed")
	}
}

// A term naming nothing is refused with a hint.
func TestTagsThatNameNothingAreRefused(t *testing.T) {
	t.Parallel()
	for name, tags := range map[string][]Tag{
		"no name":            {{Rule: []string{"dm"}}},
		"twice":              {{Name: "A"}, {Name: "a"}},
		"unknown term":       {{Name: "A", Rule: []string{"unred"}}},
		"unknown tag":        {{Name: "A", Rule: []string{"tag:B"}}},
		"picked a space":     {{Name: "A", Picked: []string{"space:Work"}}},
		"excluded a network": {{Name: "A", Excluded: []string{"protocol:WhatsApp"}}},
	} {
		if _, _, err := NewTagSet(tags); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A cycle is reported once and its internal references match nothing, so answers
// are order-independent and nothing loops.
func TestCyclesAreReportedAndMatchNothing(t *testing.T) {
	t.Parallel()
	set, warnings, err := NewTagSet([]Tag{
		{Name: "Family", Rule: []string{"dm", "not tag:Work"}},
		{Name: "Work", Rule: []string{"tag:Family"}},
		{Name: "Self", Rule: []string{"tag:Self", "group"}},
		{Name: "Fine", Rule: []string{"tag:Family"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0]+warnings[1], "Family, Work") || !strings.Contains(warnings[0]+warnings[1], "Self") {
		t.Fatalf("warnings = %q, want Family/Work and Self, once each", warnings)
	}
	// Family = dm, not (nothing) → Mom's DM; Work = (nothing) → nothing.
	if !set.Has("Family", momDM, RoomState{}) || set.Has("Work", momDM, RoomState{}) {
		t.Error("references inside the cycle still count")
	}
	if !set.Has("Self", workGrp, RoomState{}) || set.Has("Self", momDM, RoomState{}) {
		t.Error("a tag naming itself is not broken the same way")
	}
	if !set.Has("Fine", momDM, RoomState{}) {
		t.Error("a reference into a cycle from outside it is dropped too")
	}
}

// Over random reference graphs, Has agrees with a transitive-closure model and
// always terminates.
func TestTagReferencesOverRandomGraphs(t *testing.T) {
	t.Parallel()
	rooms := []RoomFacts{momDM, workGrp, botsGrp}
	terms := []string{"dm", "group", "space:Work", "protocol:WhatsApp", "room:Bots", "*"}
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 7)) // #nosec G404 -- reproducible
		n := 1 + rng.IntN(6)
		tags := make([]Tag, n)
		for i := range tags {
			tags[i].Name = fmt.Sprintf("T%d", i)
			for range rng.IntN(4) {
				var term string
				if rng.IntN(2) == 0 {
					term = fmt.Sprintf("tag:T%d", rng.IntN(n))
				} else {
					term = terms[rng.IntN(len(terms))]
				}
				if rng.IntN(3) == 0 {
					term = "not " + term
				}
				tags[i].Rule = append(tags[i].Rule, term)
			}
		}
		set, _, err := NewTagSet(tags)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		model := modelTags(tags)
		for i := range tags {
			for _, room := range rooms {
				if got, want := set.Has(tags[i].Name, room, RoomState{}), model(i, room); got != want {
					t.Fatalf("seed %d: Has(T%d, %s) = %t, model says %t; tags %+v", seed, i, room.Name, got, want, tags)
				}
			}
		}
	}
}

// modelTags breaks references between mutually reachable tags (by transitive closure)
// and evaluates the remaining DAG recursively.
func modelTags(tags []Tag) func(int, RoomFacts) bool {
	n := len(tags)
	index := func(name string) int {
		var i int
		_, _ = fmt.Sscanf(name, "T%d", &i)
		return i
	}
	reach := make([][]bool, n)
	for i := range reach {
		reach[i] = make([]bool, n)
		for _, term := range tags[i].Rule {
			if name, ok := TagOf(strings.TrimPrefix(term, "not ")); ok {
				reach[i][index(name)] = true
			}
		}
	}
	for k := range n {
		for i := range n {
			for j := range n {
				reach[i][j] = reach[i][j] || (reach[i][k] && reach[k][j])
			}
		}
	}
	var eval func(i int, room RoomFacts) bool
	eval = func(i int, room RoomFacts) bool {
		var pos, neg []bool
		for _, raw := range tags[i].Rule {
			not := strings.HasPrefix(raw, "not ")
			term := strings.TrimPrefix(raw, "not ")
			var match bool
			if name, ok := TagOf(term); ok {
				j := index(name)
				match = (!reach[i][j] || !reach[j][i]) && eval(j, room)
			} else {
				match = term == "*" || room.Names(term)
			}
			if not {
				neg = append(neg, match)
			} else {
				pos = append(pos, match)
			}
		}
		if len(pos) == 0 && len(neg) == 0 {
			return false
		}
		for _, m := range neg {
			if m {
				return false
			}
		}
		if len(pos) == 0 {
			return true
		}
		for _, m := range pos {
			if m {
				return true
			}
		}
		return false
	}
	return eval
}

// Filing writes the minimum: no pick where the rule holds, no exclude where it doesn't;
// a dropped shared entry leaves the other room's ID.
func TestFiledWritesOnlyWhatTheChoiceNeeds(t *testing.T) {
	t.Parallel()
	twin := RoomFacts{ID: "!twin:x", Name: "Standup"}
	set := mustTags(t,
		Tag{Name: "Work", Rule: []string{"space:Work"}, Excluded: []string{"room:Standup"}},
		Tag{Name: "Pins", Picked: []string{"!mom:x"}},
	)
	others := []RoomFacts{momDM, workGrp, botsGrp, twin}
	picked, excluded := set.Filed(0, workGrp, RoomState{}, true, others)
	if len(picked) != 0 || !slices.Equal(excluded, []string{"!twin:x"}) {
		t.Errorf("into Work = %v / %v, want nothing picked (the rule holds it) and the twin still excluded", picked, excluded)
	}
	picked, excluded = set.Filed(1, momDM, RoomState{}, false, others)
	if len(picked) != 0 || len(excluded) != 0 {
		t.Errorf("out of Pins = %v / %v, want both empty (the rule never held it)", picked, excluded)
	}
	picked, excluded = set.Filed(0, botsGrp, RoomState{}, false, others)
	if !slices.Equal(excluded, []string{"room:Standup", "!bots:x"}) || len(picked) != 0 {
		t.Errorf("out of Work = %v / %v, want Bots excluded by ID", picked, excluded)
	}
}

// A network archive puts a room in its followed tag (and tags naming it), never past
// an exclusion. Taking it out excludes it; putting it back writes nothing.
func TestANetworksArchiveHoldsARoomInItsTag(t *testing.T) {
	t.Parallel()
	set := mustTags(t,
		Tag{Name: "Archived"},
		Tag{Name: "Old"},
		Tag{Name: "Quiet", Rule: []string{"tag:Archived"}},
		Tag{Name: "Kept", Excluded: []string{"telegram:42/9"}},
	)
	places := Places{Tags: set, Archives: map[Protocol]string{ProtocolTelegram: "archived"}}
	facts := func(id string, archived bool) RoomFacts {
		return places.Facts(Room{ID: RoomID(id), Name: id, Archived: archived}, nil)
	}
	for _, c := range []struct {
		facts RoomFacts
		tags  []string
	}{
		{facts("telegram:42/7", true), []string{"Archived", "Quiet"}},
		{facts("telegram:42/7", false), nil},
		{facts("whatsapp:44/1@s.whatsapp.net", true), nil}, // WhatsApp's archive not followed
	} {
		if !slices.Equal(c.facts.Tags, c.tags) {
			t.Errorf("%s (archived=%q): tags %v, want %v", c.facts.ID, c.facts.ArchivedIn, c.facts.Tags, c.tags)
		}
	}
	excluded := mustTags(t, Tag{Name: "Archived", Excluded: []string{"telegram:42/7"}})
	if got := (Places{Tags: excluded, Archives: places.Archives}).Facts(Room{ID: "telegram:42/7", Archived: true}, nil); len(got.Tags) != 0 {
		t.Errorf("an excluded room archived on the network is in %v", got.Tags)
	}
	archived := facts("telegram:42/7", true)
	if picked, out := set.Filed(0, archived, RoomState{}, false, nil); len(picked) != 0 || !slices.Equal(out, []string{"telegram:42/7"}) {
		t.Errorf("out of Archived = %v / %v, want it excluded", picked, out)
	}
	if picked, out := set.Filed(0, archived, RoomState{}, true, nil); len(picked) != 0 || len(out) != 0 {
		t.Errorf("into Archived = %v / %v, want nothing written", picked, out)
	}
}

// The narrowest rule wins (room over tag, space or network; later among equals), else
// the global setting.
func TestARoomKeepsWhatItsNarrowestRuleSays(t *testing.T) {
	t.Parallel()
	room := RoomFacts{ID: "telegram:42/7", Name: "Dana", Spaces: []string{"Family"}, Tags: []string{"Archived"}, Protocol: ProtocolTelegram, Direct: true}
	for _, c := range []struct {
		rules []KeepRule
		want  int
	}{
		{nil, -1},
		{[]KeepRule{{"tag:Archived", 500}}, 500},
		{[]KeepRule{{"tag:Archived", 500}, {"space:Family", 50}}, 50},         // equals: the later
		{[]KeepRule{{"room:Dana", 10}, {"tag:Archived", 500}}, 10},            // one room over a set
		{[]KeepRule{{"telegram:42/7", -1}, {"protocol:telegram", 100}}, -1},   // by ID, keeping all
		{[]KeepRule{{"tag:Work", 5}, {"space:Work", 5}, {"!other:x", 5}}, -1}, // none name it
	} {
		if got := MessagesKept(-1, c.rules, room); got != c.want {
			t.Errorf("rules %v: keeps %d, want %d", c.rules, got, c.want)
		}
	}
}
