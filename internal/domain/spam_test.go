package domain

import (
	"slices"
	"testing"
	"time"
)

// The two lists, and the one rule that matters between them: the carve-out outranks
// everything, because an exception a rule can overturn is a suggestion.
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
	// Both halves of the toggle, and the asymmetry the key relies on.
	marked := spam.Excusing("!dm:x", false).With("!dm:x", true)
	if !marked.Lists("!dm:x") || slices.Contains(marked.Except, "!dm:x") {
		t.Errorf("marking left %v / %v", marked.Entries, marked.Except)
	}
	released := marked.With("!dm:x", false).Excusing("!dm:x", true)
	if released.Lists("!dm:x") || !slices.Contains(released.Except, "!dm:x") {
		t.Errorf("releasing left %v / %v", released.Entries, released.Except)
	}
}

// A filter identifies a message; the rules promote a room. Both halves are pinned here
// because the whole feature turns on them being different questions.
func TestSpamRulesPromote(t *testing.T) {
	t.Parallel()

	rules := SpamRules{
		Filters: []SpamFilter{
			{Name: "crypto", Words: Tracked{Words: []string{"*bitcoin*", "invest*"}}},
			{Name: "that number", Words: Tracked{Words: []string{"*"}}, From: "@spam:x"},
		},
		FirstMessage: true, Direct: true, Ratio: 0.6, Floor: 5, Window: time.Hour,
	}

	if _, ok := rules.Catches("morning all", "@dana:x"); ok {
		t.Error("an ordinary message was caught")
	}
	filter, ok := rules.Catches("double your Bitcoin", "@dana:x")
	if !ok || filter.Name != "crypto" {
		t.Fatalf("caught = %+v (%v), want the crypto filter", filter, ok)
	}
	// The sender clause: the same words from somebody else are not this filter's.
	if _, ok := rules.Catches("hello", "@dana:x"); ok {
		t.Error("a filter with a sender caught somebody else")
	}
	if filter, ok := rules.Catches("hello", "@spam:x"); !ok || filter.Name != "that number" {
		t.Errorf("caught = %+v (%v), want the sender's filter", filter, ok)
	}

	cases := map[string]struct {
		place SpamCase
		want  SpamRule
	}{
		"nothing caught promotes nothing": {SpamCase{First: true, Direct: true}, SpamNotSpam},
		"the first message wins":          {SpamCase{Filter: "crypto", First: true}, SpamFirstMessage},
		"a direct message is its sender":  {SpamCase{Filter: "crypto", Direct: true}, SpamDirect},
		"a group needs the share":         {SpamCase{Filter: "crypto"}, SpamNotSpam},
		"most of them":                    {SpamCase{Filter: "crypto", Caught: 7, Total: 10}, SpamMostly},
		"under the floor is not evidence": {SpamCase{Filter: "crypto", Caught: 3, Total: 3}, SpamNotSpam},
		"under the share":                 {SpamCase{Filter: "crypto", Caught: 3, Total: 10}, SpamNotSpam},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rule, promoted := rules.Promotes(tc.place)
			if promoted != (tc.want != SpamNotSpam) || rule != tc.want {
				t.Errorf("Promotes(%+v) = %v (%v), want %v", tc.place, rule, promoted, tc.want)
			}
		})
	}
}

// Every rule can say why, because "why is this room in Spam" is the question the place
// owes an answer to — and the answer names the filter, since that is the editable thing.
func TestEveryRuleHasAReason(t *testing.T) {
	t.Parallel()

	for _, rule := range []SpamRule{SpamByHand, SpamFirstMessage, SpamDirect, SpamMostly} {
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
