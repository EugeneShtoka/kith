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

	inv = inv.withJoined([]domain.Room{room("!a"), room("!b")})
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!b") {
		t.Fatalf("after withJoined: %v", got)
	}

	// Invitations come last: they are only ever shown by their own rail group, so their
	// position among the joined rooms is not visible.
	inv = inv.withInvites([]domain.Room{room("!i")})
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!b", "!i") {
		t.Fatalf("after withInvites: %v", got)
	}

	// A refresh of one source must not drop the other — the reason they are kept apart
	// at all.
	inv = inv.withJoined([]domain.Room{room("!a"), room("!b"), room("!c")})
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!b", "!c", "!i") {
		t.Errorf("a room-list refresh dropped the invitations: %v", got)
	}
	inv = inv.withInvites([]domain.Room{room("!i"), room("!j")})
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!b", "!c", "!i", "!j") {
		t.Errorf("a revised invitation set dropped the rooms: %v", got)
	}

	// Leaving drops it from whichever source held it, and from the union with it.
	inv = inv.without("!b")
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!c", "!i", "!j") {
		t.Errorf("after leaving a joined room: %v", got)
	}
	inv = inv.without("!i")
	if got := roomIDs(inv.all); !sameIDs(got, "!a", "!c", "!j") {
		t.Errorf("after rejecting an invitation: %v", got)
	}
	if got := inv.without("!nope"); len(got.all) != 3 {
		t.Errorf("leaving a room that is not there changed the list: %v", roomIDs(got.all))
	}
}

// Dropping a room does not write into the slice its source was handed.
func TestLeavingARoomDoesNotEditTheCallersSlice(t *testing.T) {
	t.Parallel()

	given := []domain.Room{{ID: "!a"}, {ID: "!b"}, {ID: "!c"}}
	inv := inventory{}.withJoined(given)

	_ = inv.without("!b")

	if got := roomIDs(given); !sameIDs(got, "!a", "!b", "!c") {
		t.Errorf("the slice handed to withJoined was modified: %v", got)
	}
}

// byID answers over the union, so an invitation is found as readily as a joined room.
func TestRoomLookupCoversInvitationsToo(t *testing.T) {
	t.Parallel()

	inv := inventory{}.
		withJoined([]domain.Room{{ID: "!a", Name: "Alpha"}}).
		withInvites([]domain.Room{{ID: "!i", Name: "Invited"}})

	if got, ok := inv.byID("!a"); !ok || got.Name != "Alpha" {
		t.Errorf("byID(!a) = %+v ok=%v", got, ok)
	}
	if got, ok := inv.byID("!i"); !ok || got.Name != "Invited" {
		t.Errorf("byID(!i) = %+v ok=%v — an invitation is in the list too", got, ok)
	}
	if _, ok := inv.byID("!missing"); ok {
		t.Error("byID found a room that is not there")
	}
}
