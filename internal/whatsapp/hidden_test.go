package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hiding a person recounts the rooms they wrote in, and streams the new count.
func TestHidingAPersonRecountsTheirRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a, cache, _ := offline(t)
	const room domain.RoomID = "whatsapp:15550000001/120363@g.us"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "whatsapp:15550000001/120363@g.us/1", RoomID: room, Sender: "whatsapp:15550000002@s.whatsapp.net", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "whatsapp:15550000001/120363@g.us/2", RoomID: room, Sender: "whatsapp:15550000003@s.whatsapp.net", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"whatsapp:15550000003@s.whatsapp.net"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-a.Unread():
		if u.RoomID != room || !u.Counted || u.Messages != 1 {
			t.Errorf("streamed %+v, want the room counted at 1 without the hidden person", u)
		}
	default:
		t.Fatal("hiding a person streamed no recount")
	}
}
