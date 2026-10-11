package domain

import (
	"slices"
	"testing"
	"time"
)

// The carve-out outranks the list and every rule.
func TestSpamListsAndTheCarveOut(t *testing.T) {
	t.Parallel()

	dm := RoomFacts{ID: "!dm:x", Name: "Dana", Direct: true, Protocol: ProtocolWhatsApp}
	work := RoomFacts{ID: "!w:x", Name: "Standup", Spaces: []string{"Work"}}

	spam := Spam{Entries: []string{"dm", "room:Standup"}, Except: []string{"!dm:x"}}
	if !spam.Excused(dm) {
		t.Error("a room named in except is not excused")
	}
	if spam.Excused(work) {
		t.Error("a room nobody excused is excused")
	}
	if !spam.Names(work) {
		t.Error("a room named by the list is not named")
	}
	// Both halves of the toggle.
	marked := spam.Excusing("!dm:x", false).With("!dm:x", true)
	if !marked.Lists("!dm:x") || slices.Contains(marked.Except, "!dm:x") {
		t.Errorf("marking left %v / %v", marked.Entries, marked.Except)
	}
	released := marked.With("!dm:x", false).Excusing("!dm:x", true)
	if released.Lists("!dm:x") || !slices.Contains(released.Except, "!dm:x") {
		t.Errorf("releasing left %v / %v", released.Entries, released.Except)
	}
}

// A filter identifies a message in the places it watches; its moves promote a room.
func TestSpamRulesPromote(t *testing.T) {
	t.Parallel()

	cheap := SpamMoves{FirstMessage: true, Direct: true}
	crypto := SpamFilter{Name: "crypto", Words: Tracked{Words: []string{"*bitcoin*", "invest*"}}, Moves: SpamMoves{FirstMessage: true, Direct: true, Ratio: true}}
	number := SpamFilter{Name: "that number", Words: Tracked{Words: []string{"*"}}, From: "@spam:x", Moves: cheap}
	elections := SpamFilter{Name: "elections", Words: Tracked{Words: []string{"vote"}}, In: []string{"place:SMS"}, Moves: SpamMoves{Always: true}}
	byRatio := SpamFilter{Name: "share", Words: Tracked{Words: []string{"*"}}, Moves: SpamMoves{Ratio: true}}
	rules := SpamRules{Filters: []SpamFilter{crypto, number, elections}, Ratio: 0.6, Floor: 5, Window: time.Hour}
	sms := RoomFacts{ID: "!s:x", Spaces: []string{"Google Messages"}, SpaceLabels: []string{"SMS"}}
	work := RoomFacts{ID: "!w:x", Spaces: []string{"Work"}}

	names := func(fs []SpamFilter) []string {
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
		if got := names(rules.Catching(tc.body, tc.sender, tc.room)); !slices.Equal(got, tc.want) {
			t.Errorf("Catching(%q, %s, %s) = %v, want %v", tc.body, tc.sender, tc.room.ID, got, tc.want)
		}
	}
	if !rules.CountsForRatio("bitcoin", "@dana:x", work) || rules.CountsForRatio("hello", "@spam:x", work) {
		t.Error("the ratio counts a filter that does not move by it, or skips one that does")
	}

	cases := map[string]struct {
		place  SpamCase
		want   SpamRule
		filter string
	}{
		"nothing caught promotes nothing":   {SpamCase{First: true, Direct: true}, SpamNotSpam, ""},
		"the first message wins":            {SpamCase{Filters: []SpamFilter{crypto}, First: true}, SpamFirstMessage, "crypto"},
		"a direct message is its sender":    {SpamCase{Filters: []SpamFilter{crypto}, Direct: true}, SpamDirect, "crypto"},
		"not one you wrote in":              {SpamCase{Filters: []SpamFilter{crypto}, Direct: true, Mine: true}, SpamNotSpam, ""},
		"a group needs the share":           {SpamCase{Filters: []SpamFilter{crypto}}, SpamNotSpam, ""},
		"most of them":                      {SpamCase{Filters: []SpamFilter{crypto}, Caught: 7, Total: 10}, SpamMostly, "crypto"},
		"under the floor is not evidence":   {SpamCase{Filters: []SpamFilter{crypto}, Caught: 3, Total: 3}, SpamNotSpam, ""},
		"under the share":                   {SpamCase{Filters: []SpamFilter{crypto}, Caught: 3, Total: 10}, SpamNotSpam, ""},
		"no ratio move, no ratio":           {SpamCase{Filters: []SpamFilter{number}, Caught: 7, Total: 10}, SpamNotSpam, ""},
		"no first-message move":             {SpamCase{Filters: []SpamFilter{byRatio}, First: true}, SpamNotSpam, ""},
		"no direct move":                    {SpamCase{Filters: []SpamFilter{byRatio}, Direct: true}, SpamNotSpam, ""},
		"always moves on any catch":         {SpamCase{Filters: []SpamFilter{elections}}, SpamAlways, "elections"},
		"the first filter whose moves fire": {SpamCase{Filters: []SpamFilter{number, elections}}, SpamAlways, "elections"},
		"of two that fire, the first":       {SpamCase{Filters: []SpamFilter{number, elections}, First: true}, SpamFirstMessage, "that number"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rule, filter, promoted := rules.Promotes(tc.place)
			if promoted != (tc.want != SpamNotSpam) || rule != tc.want || filter.Name != tc.filter {
				t.Errorf("Promotes(%+v) = %v %q (%v), want %v %q", tc.place, rule, filter.Name, promoted, tc.want, tc.filter)
			}
		})
	}
}

// Every rule has a reason, naming the filter.
func TestEveryRuleHasAReason(t *testing.T) {
	t.Parallel()

	for _, rule := range []SpamRule{SpamByHand, SpamFirstMessage, SpamDirect, SpamMostly, SpamAlways} {
		if reason := rule.Reason("crypto"); reason == "" {
			t.Errorf("rule %d has no reason", rule)
		}
	}
	if SpamNotSpam.Reason("") != "" {
		t.Error("a room that is not in Spam has a reason for being there")
	}
	if got := SpamFirstMessage.Reason("crypto"); got == SpamFirstMessage.Reason("") {
		t.Error("the filter's name is not in the reason")
	}
}
