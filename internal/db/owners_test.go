package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// writer is one owner's rooms: a pool to draw snapshots from.
type writer struct {
	owner domain.RoomOwner
	pool  []domain.RoomID
}

// writers are Matrix and two WhatsApp accounts whose digits begin alike, so an ID
// prefix sloppy by one character would reach the other's rooms.
func writers(sizes ...int) []writer {
	matrix := writer{owner: domain.MatrixRooms}
	a := writer{owner: domain.AccountRooms(domain.ProtocolWhatsApp, "359")}
	b := writer{owner: domain.AccountRooms(domain.ProtocolWhatsApp, "3590")}
	for i := range sizes[0] {
		id := fmt.Sprintf("!r%d:x", i)
		if i%3 == 0 {
			id = fmt.Sprintf("!v12opaque%d", i) // room v12: no server
		}
		matrix.pool = append(matrix.pool, domain.RoomID(id))
	}
	for i := range sizes[1] {
		a.pool = append(a.pool, domain.RoomID(fmt.Sprintf("whatsapp:359/1203%d@g.us", i)))
		b.pool = append(b.pool, domain.RoomID(fmt.Sprintf("whatsapp:3590/1203%d@g.us", i)))
	}
	return []writer{matrix, a, b}
}

// Any interleaving of every network's room refreshes: a room is cached exactly when
// its own writer's latest snapshot holds it (or its writer never sent one without
// it), and its history is never swept by another writer's.
func TestEachNetworksRefreshKeepsToItsOwnRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 3))
		size := 6
		if seed%10 == 0 {
			size = jsonListAt + 5 // the other way IDs are bound
		}
		cache := openTemp(t)
		ws := writers(size, size)
		want := map[domain.RoomID]bool{}
		for step := range 12 {
			w := ws[rng.IntN(len(ws))]
			var snapshot []domain.Room
			for _, id := range w.pool {
				if rng.IntN(3) != 0 {
					snapshot = append(snapshot, domain.Room{ID: id, Name: "n"})
				}
			}
			if err := cache.SaveRooms(ctx, w.owner, snapshot); err != nil {
				t.Fatalf("seed %d step %d: SaveRooms(%q): %v", seed, step, w.owner, err)
			}
			if len(snapshot) > 0 {
				for _, id := range w.pool {
					delete(want, id)
				}
			}
			for _, room := range snapshot {
				want[room.ID] = true
				if rng.IntN(4) == 0 {
					mustSave(t, cache, room.ID, domain.Message{ID: domain.EventID("$" + string(room.ID)), RoomID: room.ID, Body: "kept"})
				}
			}
		}
		rooms, err := cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[domain.RoomID]bool{}
		for i := range rooms {
			got[rooms[i].ID] = true
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("seed %d: %s was swept by a writer that does not own it", seed, id)
			}
		}
		for id := range got {
			if !want[id] {
				t.Fatalf("seed %d: %s survived its own writer's snapshot without it", seed, id)
			}
		}
	}
}

// A writer cannot save a room it does not own: the next refresh of the real owner
// would treat it as its own and, worse, this writer's would sweep it.
func TestARoomOfAnotherWriterIsRefused(t *testing.T) {
	t.Parallel()
	cache := openTemp(t)
	whatsapp := domain.RoomID("whatsapp:359/1203@g.us")
	err := cache.SaveRooms(context.Background(), domain.MatrixRooms, []domain.Room{{ID: "!a:x"}, {ID: whatsapp}})
	if err == nil || !strings.Contains(err.Error(), string(whatsapp)) {
		t.Fatalf("SaveRooms(Matrix, a WhatsApp room) = %v, want it refused, naming the room", err)
	}
	if rooms, _ := cache.Rooms(context.Background()); len(rooms) != 0 {
		t.Errorf("a refused save still wrote %v", rooms)
	}
}
