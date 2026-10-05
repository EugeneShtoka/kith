package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A space is a joined room, so /joined_rooms hands it back with everything else —
// and it must not reach the room list, where opening one shows an empty timeline
// under a rail group's own name.
func TestSpaceIDsAreWhatTheRoomListSiftsAgainst(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	if err := cache.SaveSpaces(ctx, domain.MatrixRooms, []domain.Space{
		{ID: "!work:x", Name: "Work", Children: []domain.RoomID{"!chat:x"}},
	}); err != nil {
		t.Fatalf("SaveSpaces() error = %v", err)
	}
	ids, err := cache.SpaceIDs(ctx)
	if err != nil || !ids["!work:x"] || len(ids) != 1 {
		t.Errorf("SpaceIDs() = %v, %v; want just the space", ids, err)
	}
}

// A network's archive is kept beside its rooms: set and cleared on its word, kept
// through a listing's rewrite (which says nothing of it), and kept for a room no
// listing has brought yet, which shows archived once one does.
func TestTheNetworksArchiveIsKeptBesideItsRooms(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	owner := domain.AccountRooms(domain.ProtocolTelegram, "42")
	rooms := []domain.Room{{ID: "telegram:42/7", Name: "Dana"}, {ID: "telegram:42/-11", Name: "Group"}}
	if err := c.SaveRooms(ctx, owner, rooms); err != nil {
		t.Fatal(err)
	}
	archived := func() map[domain.RoomID]bool {
		got, err := c.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[domain.RoomID]bool{}
		for _, r := range got {
			out[r.ID] = r.Archived
		}
		return out
	}
	if err := c.SetArchived(ctx, map[domain.RoomID]bool{"telegram:42/7": true, "telegram:42/99": true}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveRooms(ctx, owner, rooms); err != nil { // a listing rewrite
		t.Fatal(err)
	}
	if got := archived(); !got["telegram:42/7"] || got["telegram:42/-11"] || len(got) != 2 {
		t.Errorf("after archiving and a rewrite: %v", got)
	}
	if err := c.SaveRooms(ctx, owner, append(rooms, domain.Room{ID: "telegram:42/99", Name: "Late"})); err != nil {
		t.Fatal(err)
	}
	if got := archived(); !got["telegram:42/99"] {
		t.Errorf("a room archived before it was listed: %v", got)
	}
	if err := c.SetArchived(ctx, map[domain.RoomID]bool{"telegram:42/7": false}); err != nil {
		t.Fatal(err)
	}
	if got := archived(); got["telegram:42/7"] {
		t.Errorf("after unarchiving: %v", got)
	}
}
