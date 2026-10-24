package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// space_children.room_id deliberately has no foreign key (see the note beside the table).
func TestASpaceCanListAChildTheRoomTableHasNotSeenYet(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	// The spaces refresh lands first: children, no rooms written.
	err := cache.SaveSpaces(ctx, mxRooms, []domain.Space{{
		ID:       mx + "!work:x",
		Name:     "Work",
		Children: []domain.RoomID{mx + "!notyet:x", mx + "!alsonotyet:x"},
	}})
	if err != nil {
		t.Fatalf("SaveSpaces with unwritten child rooms = %v; the rail cannot depend on "+
			"SaveRooms having run first, because nothing orders the two", err)
	}

	spaces, err := cache.Spaces(ctx)
	if err != nil {
		t.Fatalf("Spaces() error = %v", err)
	}
	if len(spaces) != 1 || len(spaces[0].Children) != 2 {
		t.Fatalf("spaces = %+v, want one space keeping both children", spaces)
	}

	// The following room refresh finds the space membership in place.
	if err = cache.SaveRooms(ctx, mxRooms, []domain.Room{
		{ID: mx + "!notyet:x", Name: "One"}, {ID: mx + "!alsonotyet:x", Name: "Two"},
	}); err != nil {
		t.Fatalf("SaveRooms() error = %v", err)
	}
	spaces, err = cache.Spaces(ctx)
	if err != nil {
		t.Fatalf("Spaces() error = %v", err)
	}
	if len(spaces) != 1 || len(spaces[0].Children) != 2 {
		t.Errorf("spaces after the room refresh = %+v, want the same two children", spaces)
	}
}

// room_parents.space_id has no foreign key because ” records "no canonical parent",
// and SQLite exempts NULL from foreign-key checks but not ”.
func TestARoomCanNameAParentTheSpaceTableHasNotSeenYet(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomParent(ctx, mx+"!room:x", mx+"!unknownspace:x"); err != nil {
		t.Fatalf("SaveRoomParent naming an unwritten space = %v; a canonical parent is "+
			"resolved per room and need not wait for the space list", err)
	}
	parent, known, err := cache.RoomParent(ctx, mx+"!room:x")
	if err != nil || !known || parent != mx+"!unknownspace:x" {
		t.Errorf("RoomParent() = %q, %v, %v; want the space it was given", parent, known, err)
	}
}
