package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's read-only mark is kept beside it, survives a listing rewrite and is cleared
// on the network's word.
func TestAReadOnlyRoomIsKeptBesideItsRoom(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	rooms := []domain.Room{{ID: "!news:x", Name: "News"}, {ID: "!chat:x", Name: "Chat"}}
	if err := c.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
		t.Fatal(err)
	}
	readOnly := func() map[domain.RoomID]bool {
		got, err := c.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[domain.RoomID]bool{}
		for _, r := range got {
			out[r.ID] = r.ReadOnly
		}
		return out
	}
	if err := c.SetReadOnly(ctx, map[domain.RoomID]bool{"!news:x": true, "!chat:x": false}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
		t.Fatal(err)
	}
	if got := readOnly(); !got["!news:x"] || got["!chat:x"] {
		t.Errorf("after marking and a rewrite: %v", got)
	}
	if err := c.SetReadOnly(ctx, map[domain.RoomID]bool{"!news:x": false}); err != nil {
		t.Fatal(err)
	}
	if got := readOnly(); got["!news:x"] {
		t.Errorf("cleared, still read-only: %v", got)
	}
}
