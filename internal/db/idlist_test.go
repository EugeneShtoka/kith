package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An ID list longer than one statement may bind (SQLite's 32,766 variables) still
// answers: an account in that many rooms, a space of that size, a star set that big.
// Each reader that takes a list is asked with one, plus the one room that matters.
func TestLongIDListsStillAnswer(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!r:x")
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: room, Name: "R"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMembers(ctx, room, []domain.Member{{UserID: "@d:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: "$m", Sender: "@d:x", Body: "hello there"}}); err != nil {
		t.Fatal(err)
	}
	const n = 40000
	rooms := make([]domain.RoomID, 0, n+1)
	events := make([]domain.EventID, 0, n+1)
	for i := range n {
		rooms = append(rooms, domain.RoomID(fmt.Sprintf("!%d:x", i)))
		events = append(events, domain.EventID(fmt.Sprintf("$%d", i)))
	}
	rooms = append(rooms, room)
	events = append(events, "$m")

	for name, ask := range map[string]func() (int, error){
		"RoomsWith": func() (int, error) {
			got, err := cache.RoomsWith(ctx, []string{"@d:x"}, domain.TheseRooms(rooms), 10)
			return len(got), err
		},
		"SearchMessages": func() (int, error) {
			got, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "hello"}, Rooms: domain.TheseRooms(rooms), Limit: 10})
			return len(got), err
		},
		"SearchSenders": func() (int, error) {
			got, err := cache.SearchSenders(ctx, domain.TheseRooms(rooms), 10)
			return len(got), err
		},
		"RecentBodies": func() (int, error) {
			got, err := cache.RecentBodies(ctx, domain.TheseRooms(rooms), "", 10)
			return len(got), err
		},
		"SetStarred": func() (int, error) {
			if err := cache.SetStarred(ctx, room, events, 1); err != nil {
				return 0, err
			}
			ok, err := cache.IsStarred(ctx, room, "$m")
			if ok {
				return 1, err
			}
			return 0, err
		},
		"EmojiScores": func() (int, error) {
			_, err := cache.EmojiScores(ctx, domain.EmojiReaction, room, rooms, "@me:x", "")
			return 1, err
		},
	} {
		if got, err := ask(); err != nil || got == 0 {
			t.Errorf("%s over %d IDs = %d results, %v; want an answer", name, len(rooms), got, err)
		}
	}
}

// A room snapshot longer than SQLite's variable limit still sweeps the rooms it no
// longer carries.
func TestAVeryLongRoomSnapshotStillSweeps(t *testing.T) {
	t.Parallel()
	cache, ctx := openTemp(t), context.Background()
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!gone:x", Name: "Gone"}}); err != nil {
		t.Fatal(err)
	}
	rooms := make([]domain.Room, 33_000)
	for i := range rooms {
		rooms[i] = domain.Room{ID: domain.RoomID(fmt.Sprintf("!r%d:x", i)), Name: "R"}
	}
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, rooms); err != nil {
		t.Fatalf("SaveRooms(33,000) = %v", err)
	}
	got, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(rooms) {
		t.Fatalf("%d rooms after the sweep, want %d (the gone room swept)", len(got), len(rooms))
	}
}
