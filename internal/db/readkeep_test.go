package db

import (
	"slices"
	"testing"
	"time"

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
	}, map[domain.RoomID]domain.UnreadMark{held: {Marked: true, At: time.Now().UnixMilli()}, notRun: {Marked: true, At: time.Now().UnixMilli()}})
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
	if again, err := cache.KeepReads(ctx, []domain.ReadPosition{{Room: held, Event: "e1", At: 1000}}, map[domain.RoomID]domain.UnreadMark{held: {Marked: true, At: time.Now().UnixMilli()}}); err != nil || len(again) != 0 {
		t.Errorf("an older read and the same mark moved %v, %v", again, err)
	}
}

// Of the mesh's mark and the one the network set here, the later is kept: a mark
// written before the network's (a follower lagging) is not set over it, one written
// after is, and a merged room's time goes with its mark into the room it joined. A mark
// kept from the mesh is as late as its write: a merged room's older one, handed over
// after it, is not set over it.
func TestTheLaterUnreadMarkIsKept(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cache := openTemp(t)
	const (
		room       = domain.RoomID("telegram:200/300")
		lidRoom    = domain.RoomID("whatsapp:15550100/88000@lid")
		numberRoom = domain.RoomID("whatsapp:15550100/15550@s.whatsapp.net")
	)
	for _, r := range []domain.RoomID{room, lidRoom, numberRoom} {
		id := domain.ParseID(string(r))
		if err := cache.AddRooms(ctx, domain.AccountRooms(id.Network, id.Account), []domain.Room{{ID: r}}); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().UnixMilli() - 1
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room, Marked: true}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.SetUnreadMark(ctx, lidRoom, true); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.MergeRoom(ctx, domain.RoomMerge{From: lidRoom, Into: numberRoom}); err != nil {
		t.Fatal(err)
	}
	older := map[domain.RoomID]domain.UnreadMark{room: {At: before}, lidRoom: {At: before}}
	if moved, err := cache.KeepReads(ctx, nil, older); err != nil || len(moved) != 0 {
		t.Fatalf("marks written before the network's moved %v, %v", moved, err)
	}
	later := time.Now().UnixMilli() + 1000
	if moved, err := cache.KeepReads(ctx, nil, map[domain.RoomID]domain.UnreadMark{room: {At: later}, lidRoom: {At: later}}); err != nil || len(moved) != 2 {
		t.Fatalf("marks written after the network's moved %v, %v; want both rooms", moved, err)
	}
	if moved, err := cache.KeepReads(ctx, nil, map[domain.RoomID]domain.UnreadMark{lidRoom: {Marked: true, At: later - 500}}); err != nil || len(moved) != 0 {
		t.Fatalf("a merged room's mark written before the one kept moved %v, %v", moved, err)
	}
	for _, r := range []domain.RoomID{room, numberRoom} {
		if marked, err := cache.UnreadMark(ctx, r); err != nil || marked {
			t.Errorf("%s marked %v, %v; want the later mark, cleared", r, marked, err)
		}
	}
}
