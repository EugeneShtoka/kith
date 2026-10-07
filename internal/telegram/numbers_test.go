package telegram

import (
	"maps"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A listing gives the phone book the people whose number Telegram shows: a contact by
// the name you saved, anyone else by their own, and nobody without a number, deleted,
// or yourself. A listing read again replaces what the account knew.
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
	if err := cache.SetNumberNames(t.Context(), "whatsapp:111", []domain.NumberName{
		{Phone: "15550100002", Name: "Eli Saved", Rank: domain.RankSaved},
	}); err != nil {
		t.Fatal(err)
	}
	// A bridge's name for Dana yields to the name saved in Telegram's contacts.
	if err := cache.SaveMembers(t.Context(), "!dm:x", []domain.Member{{UserID: "@whatsapp_15550100001:x", DisplayName: "Dana Bridged (WA)"}}); err != nil {
		t.Fatal(err)
	}
	book, err := cache.PhoneBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Eli's own Telegram name yields to the name a WhatsApp address book saved.
	if want := (domain.PhoneBook{"15550100001": "Dana Saved", "15550100002": "Eli Saved"}); !maps.Equal(book, want) {
		t.Errorf("book = %v, want %v", book, want)
	}
	if err := a.save(t.Context(), 42, listed(42, nil), time.Now()); err != nil {
		t.Fatal(err)
	}
	if book, _ = cache.PhoneBook(t.Context()); book["15550100001"] != "Dana Bridged" {
		t.Errorf("after a listing without Dana, the bridge's name is all that is left: %v", book)
	}
}
