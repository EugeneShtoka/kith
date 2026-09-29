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
	if err := cache.SaveSpaces(ctx, []domain.Space{
		{ID: "!work:x", Name: "Work", Children: []domain.RoomID{"!chat:x"}},
	}); err != nil {
		t.Fatalf("SaveSpaces() error = %v", err)
	}
	ids, err := cache.SpaceIDs(ctx)
	if err != nil || !ids["!work:x"] || len(ids) != 1 {
		t.Errorf("SpaceIDs() = %v, %v; want just the space", ids, err)
	}
}
