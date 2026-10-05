package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat archived or unarchived on the phone (or in WhatsApp's first sync) is kept so:
// a group by its JID, a direct chat by its person, and one kith has not listed yet
// shows archived once it is.
func TestAChatArchivedOnThePhoneIsKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, _ := offline(t, account)
	changed := 0
	a.OnRoomsChanged(func() { changed++ })
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits)
	if err := cache.SaveRooms(ctx, owner, []domain.Room{{ID: waRoom, Name: "Group"}}); err != nil {
		t.Fatal(err)
	}
	archive := func(jid types.JID, archived bool) {
		a.onArchive(ctx, account, &events.Archive{JID: jid, Action: &waSyncAction.ArchiveChatAction{Archived: new(archived)}})
	}
	dm := types.NewJID("447700900001", types.DefaultUserServer)
	archive(types.NewJID("1203", types.GroupServer), true)
	archive(dm, true)
	if _, err := cache.JoinRooms(ctx, owner, []domain.RoomID{roomID(ownDigits, dm)}); err != nil { // its first message
		t.Fatal(err)
	}
	rooms, _ := cache.Rooms(ctx)
	got := map[domain.RoomID]bool{}
	for i := range rooms {
		got[rooms[i].ID] = rooms[i].Archived
	}
	if !got[waRoom] || !got[roomID(ownDigits, dm)] || changed != 2 {
		t.Errorf("archived = %v, rooms changed %d times", got, changed)
	}
	archive(types.NewJID("1203", types.GroupServer), false)
	rooms, _ = cache.Rooms(ctx)
	for i := range rooms {
		if rooms[i].ID == waRoom && rooms[i].Archived {
			t.Error("the group unarchived on the phone is still archived")
		}
	}
}
