package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A channel the account follows but a listing does not name (it left) is forgotten:
// its position, so it is not followed on connecting again, and its room; what is
// still heard from it makes no room. A listing that names it again takes it back.
func TestAChannelLeftIsNoLongerFollowed(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	a, cache := cachedAdapterWithStore(t, st)
	ctx := t.Context()
	const left = int64(77)
	room := roomID(42, -(channelMark + left))
	if err := st.SetChannelPts(ctx, 42, left, 5); err != nil {
		t.Fatal(err)
	}
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolTelegram, "42"), []domain.Room{{ID: room, Membership: domain.MembershipJoin}}); err != nil {
		t.Fatal(err)
	}

	if rooms, _ := a.Rooms(ctx); len(rooms) != 1 {
		t.Fatalf("rooms = %+v, want the channel's before the listing", rooms)
	}
	a.settleFollowed(ctx, 42, nil)
	if _, followed, _ := st.GetChannelPts(ctx, 42, left); followed {
		t.Error("the channel left is still followed")
	}
	if rooms, _ := a.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("rooms = %+v, want the channel left's gone", rooms)
	}
	msg := domain.Message{ID: inRoom(room, 9), RoomID: room, Body: "still talking", Timestamp: time.Unix(2000, 0)}
	if err := a.arrived(ctx, home, 42, msg); err != nil {
		t.Fatal(err)
	}
	if rooms, _ := a.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("a message of the channel left made a room: %+v", rooms)
	}

	a.settleFollowed(ctx, 42, []dialog{{peer: &tg.InputPeerChannel{ChannelID: left}}})
	if a.notOurs(42, room) {
		t.Error("a channel listed again is still taken as left")
	}
}

// cachedAdapterWithStore is home's adapter over a fresh cache and st.
func cachedAdapterWithStore(t *testing.T, st *Store) (*Adapter, *db.Cache) {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return New(cache, &memSecrets{values: map[string]string{}}, st, []Account{home}, nil), cache
}
