package db

import (
	"context"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's network is kept once and never rewritten, may precede the room's listing,
// and is dropped with the room.
func TestARoomsNetworkIsKeptOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x"}, {ID: mx + "!b:x"}, {ID: mx + "!c:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveInvites(ctx, mxRooms, []domain.Room{{ID: mx + "!i:x", Membership: domain.MembershipInvite}}); err != nil {
		t.Fatal(err)
	}
	unknown := func() []domain.RoomID {
		t.Helper()
		got, err := cache.RoomsOfUnknownNetwork(ctx, mxRooms)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		return got
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{mx + "!a:x", mx + "!b:x", mx + "!c:x"}) {
		t.Fatalf("not read yet = %v, want every joined room", got)
	}
	if err := cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{
		mx + "!a:x": domain.ProtocolTelegram, mx + "!b:x": domain.ProtocolMatrix, mx + "!new:x": domain.ProtocolWhatsApp, mx + "!c:x": "",
	}); err != nil {
		t.Fatal(err)
	}
	// Read again, saying otherwise: what was kept stays.
	if err := cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{mx + "!a:x": domain.ProtocolSlack}); err != nil {
		t.Fatal(err)
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{mx + "!c:x"}) {
		t.Errorf("not read yet = %v, want only the one read as nothing", got)
	}
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x"}, {ID: mx + "!b:x"}, {ID: mx + "!c:x"}, {ID: mx + "!new:x"}}); err != nil {
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
		mx + "!a:x": domain.ProtocolTelegram, mx + "!b:x": domain.ProtocolMatrix, mx + "!c:x": "", mx + "!new:x": domain.ProtocolWhatsApp,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s's network = %q, want %q", id, got[id], w)
		}
	}
	if err := cache.ForgetRooms(ctx, []domain.RoomID{mx + "!a:x"}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x"}, {ID: mx + "!b:x"}, {ID: mx + "!c:x"}, {ID: mx + "!new:x"}}); err != nil {
		t.Fatal(err)
	}
	if got := unknown(); !slices.Equal(got, []domain.RoomID{mx + "!a:x", mx + "!c:x"}) {
		t.Errorf("after leaving and rejoining !a:x, not read yet = %v, want it read again", got)
	}
}
