package slack

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
	a, cache := cached(t)
	const room domain.RoomID = "slack:T1/C1"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "slack:T1/C1/1", RoomID: room, Sender: "slack:T1/U1", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "slack:T1/C1/2", RoomID: room, Sender: "slack:T1/U2", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"slack:T1/U2"}, nil, nil); err != nil {
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
