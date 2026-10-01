package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Word completion is kept by internal/local, which hears of each cached message and
// each room whose messages changed otherwise; without that its windows go stale.
func TestTheCacheListenerHearsMessagesAndDeletions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")
	b.out.open()
	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!r:x", Name: "Room"}}); err != nil {
		t.Fatal(err)
	}
	var cached []domain.EventID
	var changed []domain.RoomID
	b.OnCached(
		func(msg domain.Message) { cached = append(cached, msg.ID) },
		func(room domain.RoomID) { changed = append(changed, room) },
	)

	b.onMessage(ctx, &event.Event{
		Type: event.EventMessage, RoomID: "!r:x", ID: "$m", Sender: "@dana:x", Timestamp: 1000,
		Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "deploying now"}},
	})
	if len(cached) != 1 || cached[0] != "$m" {
		t.Errorf("cached = %v, want the live message", cached)
	}

	b.onRedaction(ctx, &event.Event{Type: event.EventRedaction, RoomID: "!r:x",
		Sender: "@dana:x", Redacts: "$m", Timestamp: 2000})
	b.fetches.wg.Wait()
	if len(changed) != 1 || changed[0] != "!r:x" {
		t.Errorf("changed = %v, want the room whose message was deleted", changed)
	}
}

// The cache holds every network's rooms; Matrix lists only its own, or the router,
// which adds each network's, would show the others' twice.
func TestMatrixListsOnlyItsOwnCachedRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:x")
	whatsapp := domain.RoomID(domain.NativeID(domain.ProtocolWhatsApp, "359", "1203@g.us"))
	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.SaveRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, "359"), []domain.Room{{ID: whatsapp, Name: "W"}}); err != nil {
		t.Fatal(err)
	}
	for _, room := range []domain.RoomID{"!a:x", whatsapp} {
		if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: room, Notifications: 1}, 0); err != nil {
			t.Fatal(err)
		}
	}

	rooms, err := b.Rooms(ctx)
	if err != nil || len(rooms) != 1 || rooms[0].ID != "!a:x" {
		t.Errorf("Rooms = (%v, %v), want only the Matrix room", rooms, err)
	}
	unread, err := b.CachedUnread(ctx)
	if err != nil || len(unread) != 1 || unread[0].RoomID != "!a:x" {
		t.Errorf("CachedUnread = (%v, %v), want only the Matrix room's", unread, err)
	}
}
