package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A direct chat is named after the person as the address book has them, else as
// they named themselves, else as a message named them, else by their number; the chat
// with yourself is You.
func TestADirectChatIsNamedAfterThePerson(t *testing.T) {
	t.Parallel()
	known := map[string]string{"447700900111": "Dana Lee"}
	names := func(_ context.Context, p types.JID) string { return known[p.User] }
	own := self{pn: types.NewJID(ownDigits, types.DefaultUserServer)}
	for name, c := range map[string]struct {
		peer     types.JID
		fallback string
		want     string
	}{
		"known":         {types.NewJID("447700900111", types.DefaultUserServer), "", "Dana Lee"},
		"from message":  {types.NewJID("447700900222", types.DefaultUserServer), "Sam", "Sam"},
		"only a number": {types.NewJID("447700900333", types.DefaultUserServer), "", "+447700900333"},
		"yourself":      {own.pn, "Me", "You"},
		"a hidden id":   {types.NewJID("100000000000001", types.HiddenUserServer), "", ""},
	} {
		if got := chatName(context.Background(), names, own, c.peer, c.fallback); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// Names arriving after the history (as right after linking) name the account's
// direct chats again: a chat with no name gets the person's, one read as a number gets
// the name; one named from a message keeps it until a better name is known; and no
// chat is made for a contact the account never spoke with.
func TestNamesArrivingLaterNameTheChats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits)
	chat := func(user string) domain.RoomID { return roomID(ownDigits, types.NewJID(user, types.DefaultUserServer)) }
	if err := cache.AddRooms(ctx, owner, []domain.Room{
		{ID: chat("447700900111"), IsDirect: true},
		{ID: chat("447700900222"), Name: "+447700900222", IsDirect: true},
		{ID: chat("447700900333"), Name: "Sam", IsDirect: true},
		{ID: chat(ownDigits), IsDirect: true},
	}); err != nil {
		t.Fatal(err)
	}
	for user, name := range map[string]string{"447700900111": "Dana Lee", "447700900222": "Alex Kim", "447700900999": "Never Spoke"} {
		if err := client.Store.Contacts.PutContactName(ctx, types.NewJID(user, types.DefaultUserServer), name, ""); err != nil {
			t.Fatal(err)
		}
	}
	a.renameChats(ctx, account, client)
	rooms, _ := cache.Rooms(ctx)
	got := map[domain.RoomID]string{}
	for i := range rooms {
		got[rooms[i].ID] = rooms[i].Name
	}
	want := map[domain.RoomID]string{
		chat("447700900111"): "Dana Lee", chat("447700900222"): "Alex Kim", chat("447700900333"): "Sam", chat(ownDigits): "You",
	}
	if len(got) != len(want) {
		t.Errorf("chats = %v, want %v (no chat made for a contact never spoken with)", got, want)
	}
	for id, name := range want {
		if got[id] != name {
			t.Errorf("%s is %q, want %q", id, got[id], name)
		}
	}
}

// An account gives the phone book every name it knows a number by: the address book's
// as saved, else the person's own or their business's as chosen; a contact known by
// LID under its number when the store has one, and none without.
func TestAnAccountNamesTheNumbersItKnows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	phone := func(user string) types.JID { return types.NewJID(user, types.DefaultUserServer) }
	// whatsmeow stores the first name, then the full name.
	if err := client.Store.Contacts.PutContactName(ctx, phone("447700900111"), "Dana", "Dana Lee"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Store.Contacts.PutPushName(ctx, phone("447700900111"), "dana!"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Store.Contacts.PutPushName(ctx, phone("447700900222"), "Alex"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Store.Contacts.PutBusinessName(ctx, phone("447700900333"), "Kim's Bakery"); err != nil {
		t.Fatal(err)
	}
	lid, unmapped := types.NewJID("100000000000001", types.HiddenUserServer), types.NewJID("100000000000002", types.HiddenUserServer)
	if err := client.Store.LIDs.PutLIDMapping(ctx, lid, phone("447700900444")); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Contacts.PutContactName(ctx, lid, "Sam Hidden", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.Contacts.PutContactName(ctx, unmapped, "Nobody's Number", ""); err != nil {
		t.Fatal(err)
	}
	// A bridge names Alex too: an address book would outrank it, Alex's own name does not.
	if err := cache.SaveMembers(ctx, "!dm:x", []domain.Member{
		{UserID: "@whatsapp_447700900222:x", DisplayName: "Alex Bridged (WA)"},
		{UserID: "@whatsapp_447700900111:x", DisplayName: "Dana Bridged (WA)"},
	}); err != nil {
		t.Fatal(err)
	}
	a.renameChats(ctx, account, client)
	dir, err := cache.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for number, want := range map[string]string{
		"447700900111": "Dana Lee", "447700900222": "Alex Bridged", "447700900333": "Kim's Bakery", "447700900444": "Sam Hidden",
		"100000000000002": "", // a LID with no number names no number
	} {
		if got, _ := dir.Named("+" + number); got != want {
			t.Errorf("+%s is named %q, want %q", number, got, want)
		}
	}
}
