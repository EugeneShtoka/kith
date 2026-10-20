package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hiding a person recounts the rooms they wrote in, and streams the new count.
func TestHidingAPersonRecountsTheirRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache, err := db.Open(ctx, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, &memSecrets{values: map[string]string{}}, nil, nil, nil)
	const room domain.RoomID = "telegram:1001/2002"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "telegram:1001/2002/1", RoomID: room, Sender: "telegram:3003", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "telegram:1001/2002/2", RoomID: room, Sender: "telegram:4004", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"telegram:4004"}, nil, nil); err != nil {
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
