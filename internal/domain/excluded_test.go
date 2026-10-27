package domain

import (
	"slices"
	"testing"
)

// onCard is a profile's card test standing in for a phonebook: the person whose IDs
// include one of card has a card that meets the profile's field.
func onCard(card ...string) ExcludedPeople {
	return ExcludedPeople{Card: func(ids []string) bool {
		return slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(card, id) })
	}}
}

// A profile's card test is asked with every ID the person goes by, followed through the
// directory and the identity groups both, so a group joining two of the directory's
// people carries a card on either side to the other. You are never among them, however
// linked to a card that meets the test.
func TestExcludedPeopleFollowEveryLinkButNeverHideYou(t *testing.T) {
	t.Parallel()
	dir := NewDirectory(nil, []PersonLink{
		{Source: "book", ID: "telegram:1", Other: "matrix:@tg_1:x"},
		{Source: "book", ID: "whatsapp:15550000001@s.whatsapp.net", Other: "matrix:@wa_1:x"},
		{Source: "book", ID: "matrix:@me:x", Other: "telegram:9"},
	})
	groups := [][]string{{"matrix:@tg_1:x", "matrix:@wa_1:x"}}
	selves := []string{"matrix:@me:x"}

	h := NewExcludedPeople(onCard("telegram:1"), groups, dir, selves)
	for _, id := range []string{"telegram:1", "matrix:@tg_1:x", "matrix:@wa_1:x", "whatsapp:15550000001@s.whatsapp.net"} {
		if !h.Has(id) {
			t.Errorf("%s is the person whose card meets the test, through a link: not hidden", id)
		}
	}
	if h.Has("matrix:@bob:x") || h.Empty() {
		t.Errorf("Bob hidden = %v, empty = %v", h.Has("matrix:@bob:x"), h.Empty())
	}

	mine := NewExcludedPeople(onCard("telegram:9", "matrix:@me:x"), groups, dir, selves)
	for _, id := range []string{"matrix:@me:x", "telegram:9"} {
		if mine.Has(id) {
			t.Errorf("%s is you, hidden by a card that names you", id)
		}
	}

	ids := NewExcludedPeople(ExcludedPeople{IDs: []string{"matrix:@tg_1:x"}}, groups, dir, selves)
	if !ids.Has("whatsapp:15550000001@s.whatsapp.net") || ids.Has("matrix:@me:x") {
		t.Errorf("an ID excluded: the person through every link hidden %v, you hidden %v",
			ids.Has("whatsapp:15550000001@s.whatsapp.net"), ids.Has("matrix:@me:x"))
	}
	if !NewExcludedPeople(ExcludedPeople{}, groups, dir, selves).Empty() {
		t.Error("nothing excluded is not empty")
	}
}
