package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// "This room has no canonical parent" is an answer, not a missing one — most rooms
// you filed into a space yourself have none — so it has to be storable. Without
// that, every rail rebuild would re-ask the homeserver about every such room forever.
func TestRoomParentRemembersAbsenceAsWellAsPresence(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if _, known, err := cache.RoomParent(ctx, "!never:x"); err != nil || known {
		t.Fatalf("unasked room = known %v, err %v; want unknown", known, err)
	}

	if err := cache.SaveRoomParent(ctx, "!lives:x", "!space:x"); err != nil {
		t.Fatalf("SaveRoomParent() error = %v", err)
	}
	if err := cache.SaveRoomParent(ctx, "!orphan:x", ""); err != nil {
		t.Fatalf("SaveRoomParent(none) error = %v", err)
	}

	parent, known, err := cache.RoomParent(ctx, "!lives:x")
	if err != nil || !known || parent != "!space:x" {
		t.Errorf("RoomParent(lives) = %q, %v, %v; want the space, known", parent, known, err)
	}
	parent, known, err = cache.RoomParent(ctx, "!orphan:x")
	if err != nil || !known || parent != "" {
		t.Errorf("RoomParent(orphan) = %q, %v, %v; want empty *and* known", parent, known, err)
	}

	all, err := cache.RoomParents(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("RoomParents() = %v, %v; want both rooms", all, err)
	}
	if all["!orphan:x"] != "" {
		t.Errorf("orphan mapped to %q, want empty", all["!orphan:x"])
	}
}

// A room can be moved between spaces, and the answer is replaced rather than
// duplicated.
func TestSaveRoomParentReplaces(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	for _, space := range []domain.SpaceID{"!first:x", "!second:x"} {
		if err := cache.SaveRoomParent(ctx, "!room:x", space); err != nil {
			t.Fatalf("SaveRoomParent(%s) error = %v", space, err)
		}
	}
	parent, _, err := cache.RoomParent(ctx, "!room:x")
	if err != nil || parent != "!second:x" {
		t.Errorf("RoomParent() = %q, %v; want the newer space", parent, err)
	}
}
