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

	// A cold cache has no invitations, which is not an error.
	got, err := cache.Invites(ctx)
	if err != nil {
		t.Fatalf("Invites on a cold cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cold cache returned %d invitations", len(got))
	}

	want := []domain.Room{
		{ID: "!i1:x", Name: "Design Review", InvitedBy: "@alice:x"},
		{ID: "!i2:x", Name: "Dana", InvitedBy: "@bob:x", IsDirect: true},
	}
	if serr := cache.SaveInvites(ctx, want); serr != nil {
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
		// Every row read back is an invitation, whatever the column layout.
		if !r.IsInvite() {
			t.Errorf("%s did not read back as an invitation", r.ID)
		}
	}
	if r := byID["!i1:x"]; r.Name != "Design Review" || r.InvitedBy != "@alice:x" || r.IsDirect {
		t.Errorf("!i1:x = %+v", r)
	}
	if r := byID["!i2:x"]; !r.IsDirect || r.InvitedBy != "@bob:x" {
		t.Errorf("!i2:x = %+v", r)
	}
}

// The set is a snapshot, not an upsert: a room that has dropped out of it was
// answered and must disappear, and an empty set clears the table.
func TestSaveInvitesIsASnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	if err := cache.SaveInvites(ctx, []domain.Room{
		{ID: "!i1:x", Name: "One"}, {ID: "!i2:x", Name: "Two"},
	}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	// One answered elsewhere: the next set has only the other.
	if err := cache.SaveInvites(ctx, []domain.Room{{ID: "!i2:x", Name: "Two"}}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	got, err := cache.Invites(ctx)
	if err != nil {
		t.Fatalf("Invites: %v", err)
	}
	if len(got) != 1 || got[0].ID != "!i2:x" {
		t.Fatalf("after a shrinking snapshot: %+v, want just !i2:x", got)
	}
	// All answered.
	if serr := cache.SaveInvites(ctx, nil); serr != nil {
		t.Fatalf("SaveInvites(nil): %v", serr)
	}
	if got, err = cache.Invites(ctx); err != nil || len(got) != 0 {
		t.Fatalf("an empty snapshot should clear the table, got %+v (err %v)", got, err)
	}
}

// A joined-room snapshot and an invitation snapshot are independent: writing one
// must not touch the other. This is the reason invitations are not in `rooms`.
func TestRoomsAndInvitesAreIndependent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveInvites(ctx, []domain.Room{{ID: "!i1:x", Name: "Invited"}}); err != nil {
		t.Fatalf("SaveInvites: %v", err)
	}
	if err := cache.SaveRooms(ctx, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
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
