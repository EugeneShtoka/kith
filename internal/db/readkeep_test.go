package db

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A read made on another bridge moves the cache's position forward only, and only for
// a room the cache holds, a merged room's filed under the room it joined: a position
// held further (the network's own) stays, a room of an account this bridge does not run
// gets none, and a mark is set. HeldReads answers alike, a merged room as the room it
// joined.
func TestReadsMadeElsewhereMoveTheCacheForwardOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cache := openTemp(t)
	const (
		held    = domain.RoomID("telegram:200/300")
		further = domain.RoomID("telegram:200/301")
		notRun  = domain.RoomID("slack:T1/C1")
	)
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolTelegram, "200"), []domain.Room{{ID: held}, {ID: further}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, "15550100"), []domain.Room{{ID: lidRoom}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.MergeRoom(ctx, domain.RoomMerge{From: lidRoom, Into: numberRoom}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: further, ReadEvent: "n9"}, 9000); err != nil {
		t.Fatal(err)
	}
	moved, err := cache.KeepReads(ctx, []domain.ReadPosition{
		{Room: held, Event: "e5", At: 5000},
		{Room: further, Event: "e6", At: 6000},
		{Room: notRun, Event: "e7", At: 7000},
		{Room: lidRoom, Event: "e8", At: 8000},
		{Room: held, Root: "r1", Event: "e4", At: 4000},
	}, map[domain.RoomID]bool{held: true, notRun: true})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(moved)
	if want := []domain.RoomID{held, numberRoom}; !slices.Equal(moved, want) {
		t.Errorf("moved %v, want %v", moved, want)
	}
	got, err := cache.HeldReads(ctx, []domain.ReadPosition{{Room: held}, {Room: further}, {Room: notRun}, {Room: lidRoom}, {Room: held, Root: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ReadPosition{
		{Room: held, Event: "e5", At: 5000}, {Room: further, Event: "n9", At: 9000},
		{Room: numberRoom, Event: "e8", At: 8000}, {Room: held, Root: "r1", Event: "e4", At: 4000},
	}
	if !slices.Equal(got, want) {
		t.Errorf("HeldReads = %+v\nwant %+v", got, want)
	}
	unread, err := cache.Unread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unread {
		if u.RoomID == notRun || u.Marked != (u.RoomID == held) {
			t.Errorf("unread state %+v: want the held room alone marked, none of a room not run", u)
		}
	}
	if again, err := cache.KeepReads(ctx, []domain.ReadPosition{{Room: held, Event: "e1", At: 1000}}, map[domain.RoomID]bool{held: true}); err != nil || len(again) != 0 {
		t.Errorf("an older read and the same mark moved %v, %v", again, err)
	}
}
