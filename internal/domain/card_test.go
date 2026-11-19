package domain

import (
	"slices"
	"testing"
)

// A card's identifiers name the people kith reaches, each in kith's form, and those
// people are the card's identifiers again: a number as its identifier, every network's
// ID as that network's person; an email, a blank or a number with no digits names no one.
func TestACardsIdentifiersAreItsPeople(t *testing.T) {
	t.Parallel()
	card := map[string][]string{
		"phone":     {"+1 555 010-0001", "x"},
		"email":     {"dana@example.com"},
		"matrix":    {"@dana:example.org", " "},
		"telegram":  {"77"},
		"whatsapp":  {"lid-123"},
		"slack":     {"T1.U2"},
		"messenger": {"1001"},
		"instagram": {"2002"},
		"signal":    {"0f3e9a52-6b1c-4d0e-9a7f-3c2b1a0d9e8f"},
	}
	got := CardPeople(card)
	want := []string{
		"instagram:2002", "matrix:@dana:example.org", "messenger:1001", "tel:15550100001",
		"signal:0f3e9a52-6b1c-4d0e-9a7f-3c2b1a0d9e8f", "slack:T1.U2", "telegram:77", "whatsapp:lid-123",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("CardPeople = %v, want %v", got, want)
	}
	back := CardIdentifiers(got)
	for key, values := range map[string][]string{
		"phone": {"+15550100001"}, "matrix": {"@dana:example.org"}, "telegram": {"77"}, "whatsapp": {"lid-123"}, "slack": {"T1.U2"},
		"messenger": {"1001"}, "instagram": {"2002"}, "signal": {"0f3e9a52-6b1c-4d0e-9a7f-3c2b1a0d9e8f"},
	} {
		if !slices.Equal(back[key], values) {
			t.Errorf("%s back = %v, want %v", key, back[key], values)
		}
	}
}
