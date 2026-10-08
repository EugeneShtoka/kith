package telegram

import (
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A listing gives the directory every user it carries, by their ID: a contact by the
// name you saved, anyone else by their own, and linked to their number where
// Telegram shows it; nobody deleted, nor yourself. A listing read again replaces what
// the account knew.
func TestAListingNamesTheNumbersItShows(t *testing.T) {
	t.Parallel()
	users := map[int64]*tg.User{
		7:  {ID: 7, FirstName: "Dana", LastName: "Saved", Phone: "15550100001", Contact: true},
		8:  {ID: 8, FirstName: "Eli", Phone: "15550100002"},
		9:  {ID: 9, FirstName: "No", LastName: "Number"},
		10: {ID: 10, FirstName: "Gone", Phone: "15550100003", Deleted: true},
		42: {ID: 42, FirstName: "Me", Phone: "15550100009", Self: true},
	}
	ent := peer.NewEntities(users, nil, nil)
	dialogs := []dialog{{peer: &tg.InputPeerUser{UserID: 7}, entities: ent}, {peer: &tg.InputPeerUser{UserID: 8}, entities: ent}}
	a, cache := cachedAdapter(t, &memSecrets{values: map[string]string{}})
	if err := a.save(t.Context(), 42, listed(42, dialogs), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cache.SetPeople(t.Context(), "whatsapp:111", []domain.PersonName{
		{ID: domain.PhoneID("15550100002"), Name: "Eli Saved", Rank: domain.RankSaved},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// A bridge's name for Dana yields to the name saved in Telegram's contacts.
	if err := cache.SaveMembers(t.Context(), "!dm:x", []domain.Member{{UserID: "@whatsapp_15550100001:x", DisplayName: "Dana Bridged (WA)"}}); err != nil {
		t.Fatal(err)
	}
	// book is the names the directory gives the numbers 15550100001-9, the named ones.
	book := func() map[string]string {
		dir, err := cache.Directory(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for n := 1; n <= 9; n++ {
			number := "1555010000" + strconv.Itoa(n)
			if name, ok := dir.Named("+" + number); ok {
				got[number] = name
			}
		}
		return got
	}
	// Eli's own Telegram name yields to the name a WhatsApp address book saved.
	if want := (map[string]string{"15550100001": "Dana Saved", "15550100002": "Eli Saved"}); !maps.Equal(book(), want) {
		t.Errorf("book = %v, want %v", book(), want)
	}
	// Each user is named by their ID too, a number or none: the one without a number
	// by their own name, Eli's ID by the name a WhatsApp address book saved for the
	// number Telegram links it to.
	dir, err := cache.Directory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{personID(9): "No Number", personID(8): "Eli Saved", personID(7): "Dana Saved", personID(10): "", personID(42): ""} {
		if got, _, _ := dir.Name(id); got != want {
			t.Errorf("%s is %q, want %q", id, got, want)
		}
	}
	if err := a.save(t.Context(), 42, listed(42, nil), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := book(); got["15550100001"] != "Dana Bridged" {
		t.Errorf("after a listing without Dana, the bridge's name is all that is left: %v", got)
	}
}
