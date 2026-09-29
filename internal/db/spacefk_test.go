package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// space_children.room_id carries no foreign key, and that is a decision (see the note
// beside the table).
func TestASpaceCanListAChildTheRoomTableHasNotSeenYet(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	// The spaces refresh landing first: a space with children, and no rooms written.
	err := cache.SaveSpaces(ctx, []domain.Space{{
		ID:       "!work:x",
		Name:     "Work",
		Children: []domain.RoomID{"!notyet:x", "!alsonotyet:x"},
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

	// And the room refresh that follows makes them ordinary rooms, with the space
	// membership already in place.
	if err = cache.SaveRooms(ctx, []domain.Room{
		{ID: "!notyet:x", Name: "One"}, {ID: "!alsonotyet:x", Name: "Two"},
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

// room_parents.space_id carries no foreign key for a different reason: the empty
// string is a recorded answer ("asked, and there is no canonical parent"), and
// SQLite exempts NULL from foreign-key checks but not ”.
func TestARoomCanNameAParentTheSpaceTableHasNotSeenYet(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomParent(ctx, "!room:x", "!unknownspace:x"); err != nil {
		t.Fatalf("SaveRoomParent naming an unwritten space = %v; a canonical parent is "+
			"resolved per room and need not wait for the space list", err)
	}
	parent, known, err := cache.RoomParent(ctx, "!room:x")
	if err != nil || !known || parent != "!unknownspace:x" {
		t.Errorf("RoomParent() = %q, %v, %v; want the space it was given", parent, known, err)
	}
}
