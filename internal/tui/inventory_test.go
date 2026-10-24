package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func roomIDs(rooms []domain.Room) []domain.RoomID {
	out := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		out[i] = rooms[i].ID
	}
	return out
}

func sameIDs(a []domain.RoomID, b ...domain.RoomID) bool { return slices.Equal(a, b) }

// The union is whatever the two sources say, after every way of changing them.
func TestTheRoomUnionFollowsItsSources(t *testing.T) {
	t.Parallel()

	room := func(id string) domain.Room { return domain.Room{ID: domain.RoomID(id)} }
	var inv inventory

	inv = inv.withJoined([]domain.Room{room(mx + "!a"), room(mx + "!b")})
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!b") {
		t.Fatalf("after withJoined: %v", got)
	}

	// Invitations come last; only their own rail group shows them, so their position is unseen.
	inv = inv.withInvites([]domain.Room{room(mx + "!i")})
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!b", mx+"!i") {
		t.Fatalf("after withInvites: %v", got)
	}

	// Refreshing one source must not drop the other; that is why they are kept apart.
	inv = inv.withJoined([]domain.Room{room(mx + "!a"), room(mx + "!b"), room(mx + "!c")})
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!b", mx+"!c", mx+"!i") {
		t.Errorf("a room-list refresh dropped the invitations: %v", got)
	}
	inv = inv.withInvites([]domain.Room{room(mx + "!i"), room(mx + "!j")})
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!b", mx+"!c", mx+"!i", mx+"!j") {
		t.Errorf("a revised invitation set dropped the rooms: %v", got)
	}

	// Leaving drops it from whichever source held it, and from the union with it.
	inv = inv.without(mx + "!b")
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!c", mx+"!i", mx+"!j") {
		t.Errorf("after leaving a joined room: %v", got)
	}
	inv = inv.without(mx + "!i")
	if got := roomIDs(inv.all); !sameIDs(got, mx+"!a", mx+"!c", mx+"!j") {
		t.Errorf("after rejecting an invitation: %v", got)
	}
	if got := inv.without(mx + "!nope"); len(got.all) != 3 {
		t.Errorf("leaving a room that is not there changed the list: %v", roomIDs(got.all))
	}
}

// Dropping a room does not write into the slice its source was handed.
func TestLeavingARoomDoesNotEditTheCallersSlice(t *testing.T) {
	t.Parallel()

	given := []domain.Room{{ID: mx + "!a"}, {ID: mx + "!b"}, {ID: mx + "!c"}}
	inv := inventory{}.withJoined(given)

	_ = inv.without(mx + "!b")

	if got := roomIDs(given); !sameIDs(got, mx+"!a", mx+"!b", mx+"!c") {
		t.Errorf("the slice handed to withJoined was modified: %v", got)
	}
}

// byID answers over the union, so an invitation is found as readily as a joined room.
func TestRoomLookupCoversInvitationsToo(t *testing.T) {
	t.Parallel()

	inv := inventory{}.
		withJoined([]domain.Room{{ID: mx + "!a", Name: "Alpha"}}).
		withInvites([]domain.Room{{ID: mx + "!i", Name: "Invited"}})

	if got, ok := inv.byID(mx + "!a"); !ok || got.Name != "Alpha" {
		t.Errorf("byID(!a) = %+v ok=%v", got, ok)
	}
	if got, ok := inv.byID(mx + "!i"); !ok || got.Name != "Invited" {
		t.Errorf("byID(!i) = %+v ok=%v — an invitation is in the list too", got, ok)
	}
	if _, ok := inv.byID(mx + "!missing"); ok {
		t.Error("byID found a room that is not there")
	}
}
