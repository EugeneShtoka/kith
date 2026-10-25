package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestInvitesRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	got, err := cache.Invites(ctx)
	if err != nil {
		t.Fatalf("Invites on a cold cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cold cache returned %d invitations", len(got))
	}

	want := []domain.Room{
		{ID: mx + "!i1:x", Name: "Design Review", InvitedBy: "matrix:@alice:x"},
		{ID: mx + "!i2:x", Name: "Dana", InvitedBy: "matrix:@bob:x", IsDirect: true},
	}
	if serr := cache.SaveInvites(ctx, mxRooms, want); serr != nil {
		t.Fatalf("SaveInvites: %v", serr)
	}
	got, err = cache.Invites(ctx)
	if err != nil {
		t.Fatalf("Invites: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d invitations, want 2", len(got))
	}
	byID := map[domain.RoomID]domain.Room{}
	for _, r := range got {
		byID[r.ID] = r
		if !r.IsInvite() {
			t.Errorf("%s did not read back as an invitation", r.ID)
		}
	}
	if r := byID[mx+"!i1:x"]; r.Name != "Design Review" || r.InvitedBy != "matrix:@alice:x" || r.IsDirect {
		t.Errorf("!i1:x = %+v", r)
	}
	if r := byID[mx+"!i2:x"]; !r.IsDirect || r.InvitedBy != "matrix:@bob:x" {
		t.Errorf("!i2:x = %+v", r)
	}
}

// SaveInvites replaces the set: dropped rooms disappear and an empty set clears it.
func TestSaveInvitesIsASnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveInvites(ctx, mxRooms, []domain.Room{
		{ID: mx + "!i1:x", Name: "One"}, {ID: mx + "!i2:x", Name: "Two"},
	}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	if err := cache.SaveInvites(ctx, mxRooms, []domain.Room{{ID: mx + "!i2:x", Name: "Two"}}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	got, err := cache.Invites(ctx)
	if err != nil {
		t.Fatalf("Invites: %v", err)
	}
	if len(got) != 1 || got[0].ID != mx+"!i2:x" {
		t.Fatalf("after a shrinking snapshot: %+v, want just !i2:x", got)
	}
	if serr := cache.SaveInvites(ctx, mxRooms, nil); serr != nil {
		t.Fatalf("SaveInvites(nil): %v", serr)
	}
	if got, err = cache.Invites(ctx); err != nil || len(got) != 0 {
		t.Fatalf("an empty snapshot should clear the table, got %+v (err %v)", got, err)
	}
}

// Saving joined rooms and saving invites don't touch each other.
func TestRoomsAndInvitesAreIndependent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveInvites(ctx, mxRooms, []domain.Room{{ID: mx + "!i1:x", Name: "Invited"}}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	invites, err := cache.Invites(ctx)
	if err != nil || len(invites) != 1 {
		t.Fatalf("a room snapshot dropped the invitations: %+v (err %v)", invites, err)
	}
	rooms, err := cache.Rooms(ctx)
	if err != nil || len(rooms) != 1 {
		t.Fatalf("rooms = %+v (err %v)", rooms, err)
	}
	if rooms[0].IsInvite() {
		t.Error("a joined room must not read back as an invitation")
	}
}
