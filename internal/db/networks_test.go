package db

import (
	"context"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's network is kept once read and never rewritten; rooms not read yet are
// exactly the joined ones with none kept; a network read before the room is listed is
// kept for it; a room forgotten takes its network with it.
func TestARoomsNetworkIsKeptOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}, {ID: "!c:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveInvites(ctx, []domain.Room{{ID: "!i:x", Membership: domain.MembershipInvite}}); err != nil {
		t.Fatal(err)
	}
	unknown := func() []domain.RoomID {
		t.Helper()
		got, err := cache.RoomsOfUnknownNetwork(ctx, domain.MatrixRooms)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		return got
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{"!a:x", "!b:x", "!c:x"}) {
		t.Fatalf("not read yet = %v, want every joined room", got)
	}
	if err := cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{
		"!a:x": domain.ProtocolTelegram, "!b:x": domain.ProtocolMatrix, "!new:x": domain.ProtocolWhatsApp, "!c:x": "",
	}); err != nil {
		t.Fatal(err)
	}
	// Read again, saying otherwise: what was kept stays.
	if err := cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{"!a:x": domain.ProtocolSlack}); err != nil {
		t.Fatal(err)
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{"!c:x"}) {
		t.Errorf("not read yet = %v, want only the one read as nothing", got)
	}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}, {ID: "!c:x"}, {ID: "!new:x"}}); err != nil {
		t.Fatal(err)
	}
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[domain.RoomID]domain.Protocol{}
	for _, r := range rooms {
		got[r.ID] = r.Network
	}
	want := map[domain.RoomID]domain.Protocol{
		"!a:x": domain.ProtocolTelegram, "!b:x": domain.ProtocolMatrix, "!c:x": "", "!new:x": domain.ProtocolWhatsApp,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s's network = %q, want %q", id, got[id], w)
		}
	}
	if err := cache.ForgetRooms(ctx, []domain.RoomID{"!a:x"}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}, {ID: "!c:x"}, {ID: "!new:x"}}); err != nil {
		t.Fatal(err)
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{"!a:x", "!c:x"}) {
		t.Errorf("after leaving and rejoining !a:x, not read yet = %v, want it read again", got)
	}
}
