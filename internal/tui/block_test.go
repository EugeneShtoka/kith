package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// The block row names the person by their card's IDs on blockable networks and by the
// ID their card was opened on, when that is on one: Messenger, Instagram and Signal have no
// phonebook key, so that ID alone names them there.
func TestTheBlockRowNamesThePersonByTheirCardAndTheirID(t *testing.T) {
	t.Parallel()
	card := phonebook.Card{IDs: phonebook.Identifiers{"telegram": {"77"}, "matrix": {"@dana:example.org"}}}
	for _, c := range []struct {
		id   string
		card phonebook.Card
		want []string
	}{
		{"messenger:201", phonebook.Card{}, []string{"messenger:201"}},
		{"signal:00000000-0000-4000-8000-000000000002", phonebook.Card{}, []string{"signal:00000000-0000-4000-8000-000000000002"}},
		{"instagram:2000", card, []string{"telegram:77", "instagram:2000"}},
		{"telegram:77", card, []string{"telegram:77"}},
		{"matrix:@dana:example.org", card, []string{"telegram:77"}},
		{"matrix:@dana:example.org", phonebook.Card{}, nil},
	} {
		if got := blockTargets(personState{id: c.id, card: c.card}); !slices.Equal(got, c.want) {
			t.Errorf("%s with %v: %v, want %v", c.id, c.card.IDs, got, c.want)
		}
	}
}
