package domain

import (
	"reflect"
	"testing"
)

// Ranked spaces come out in the user's order, not the room's.
func TestOrderSpacesPutsRankedSpacesFirstInTheRankedOrder(t *testing.T) {
	t.Parallel()

	// As a room's spaces arrive: alphabetical.
	spaces := []string{"Colleagues", "Social", "WhatsApp UK", "Work"}
	got := OrderSpaces(spaces, []string{"Work", "Colleagues"})
	want := []string{"Work", "Colleagues", "Social", "WhatsApp UK"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OrderSpaces() = %v, want %v", got, want)
	}
	// The input slice is not modified.
	if spaces[0] != "Colleagues" {
		t.Errorf("input was reordered in place: %v", spaces)
	}
}

// Unranked spaces keep their input order.
func TestOrderSpacesLeavesTheRestAlone(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		spaces, priority, want []string
	}{
		"nothing ranked":    {[]string{"Bravo", "Alpha"}, nil, []string{"Bravo", "Alpha"}},
		"none of them here": {[]string{"Bravo", "Alpha"}, []string{"Work"}, []string{"Bravo", "Alpha"}},
		"one of them":       {[]string{"Bravo", "Alpha"}, []string{"Alpha"}, []string{"Alpha", "Bravo"}},
		"single space":      {[]string{"Only"}, []string{"Other"}, []string{"Only"}},
		"case and padding":  {[]string{"Work Chat", "Alpha"}, []string{"  work chat "}, []string{"Work Chat", "Alpha"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := OrderSpaces(tc.spaces, tc.priority); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("OrderSpaces(%v, %v) = %v, want %v", tc.spaces, tc.priority, got, tc.want)
			}
		})
	}
}

// Duplicate or blank priority entries never drop or duplicate a space.
func TestOrderSpacesIsAPermutation(t *testing.T) {
	t.Parallel()

	spaces := []string{"Alpha", "Bravo", "Charlie"}
	got := OrderSpaces(spaces, []string{"Charlie", "", "Charlie", "Alpha"})
	want := []string{"Charlie", "Alpha", "Bravo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OrderSpaces() = %v, want %v", got, want)
	}
}
