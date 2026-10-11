package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Direct chat name order: address book, push name, message name, number; own chat is You.
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

// Late-arriving names rename direct chats (a message's name stays until a better one)
// and create no chat for a contact never spoken with.
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

// The phone book gets every known number's name (saved or chosen); LID contacts go
// under their number when known.
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
	// A bridge also names Alex: an address book name would outrank it, a push name does not.
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
	// A LID resolves to its mapped number's name; an unmapped LID to none.
	for id, want := range map[string]string{
		"whatsapp:" + lid.String():      "Sam Hidden",
		"whatsapp:" + unmapped.String(): "",
	} {
		if got, _, _ := dir.Name(id); got != want {
			t.Errorf("%s is %q, want %q", id, got, want)
		}
	}
}
