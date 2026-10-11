package db

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The two directions of one link are written by separate calls, so they must commute.
func TestChainLinksSurviveBeingWrittenInEitherOrder(t *testing.T) {
	t.Parallel()

	for _, order := range []struct {
		name  string
		first domain.RoomID // the successor whose predecessor is learned first
	}{
		{"newest first", "!c:x"},
		{"oldest first", "!b:x"},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			cache, ctx := openTemp(t), context.Background()

			links := map[domain.RoomID]domain.RoomID{"!b:x": "!a:x", "!c:x": "!b:x"}
			order2 := []domain.RoomID{"!c:x", "!b:x"}
			if order.first == "!b:x" {
				order2 = []domain.RoomID{"!b:x", "!c:x"}
			}
			for _, successor := range order2 {
				predecessor := links[successor]
				if err := cache.SaveRoomUpgrade(ctx, successor, RoomUpgrade{Predecessor: predecessor}); err != nil {
					t.Fatalf("SaveRoomUpgrade(%s) error = %v", successor, err)
				}
				if err := cache.SaveRoomUpgrade(ctx, predecessor, RoomUpgrade{Replacement: successor}); err != nil {
					t.Fatalf("SaveRoomUpgrade(%s) error = %v", predecessor, err)
				}
			}

			// B is both successor and predecessor, so its row is written twice.
			up, known, err := cache.RoomUpgrade(ctx, "!b:x")
			if err != nil || !known {
				t.Fatalf("RoomUpgrade(!b:x) = known %v, err %v; want known", known, err)
			}
			if up.Predecessor != "!a:x" {
				t.Errorf("!b:x predecessor = %q, want !a:x — the second write blanked the first", up.Predecessor)
			}
			if up.Replacement != "!c:x" {
				t.Errorf("!b:x replacement = %q, want !c:x", up.Replacement)
			}

			chain, err := cache.RoomChain(ctx, "!c:x")
			if err != nil {
				t.Fatalf("RoomChain() error = %v", err)
			}
			want := []domain.RoomID{"!c:x", "!b:x", "!a:x"}
			if !sameRooms(chain, want) {
				t.Errorf("RoomChain(!c:x) = %v, want %v", chain, want)
			}
		})
	}
}

// A room in no chain gets no row and no error; RoomChain runs on every search.
func TestARoomInNoChainIsItsOwnWholeChain(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if _, known, err := cache.RoomUpgrade(ctx, "!plain:x"); err != nil || known {
		t.Fatalf("unlinked room = known %v, err %v; want unknown", known, err)
	}
	chain, err := cache.RoomChain(ctx, "!plain:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if !sameRooms(chain, []domain.RoomID{"!plain:x"}) {
		t.Errorf("RoomChain(unlinked) = %v, want just itself", chain)
	}
}

// Two rooms naming each other as predecessor form a cycle; the walk stops at rooms it
// has seen, not only at the hop limit.
func TestACycleInTheCacheDoesNotSpin(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomUpgrade(ctx, "!a:x", RoomUpgrade{Predecessor: "!b:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}
	if err := cache.SaveRoomUpgrade(ctx, "!b:x", RoomUpgrade{Predecessor: "!a:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}

	chain, err := cache.RoomChain(ctx, "!a:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if !sameRooms(chain, []domain.RoomID{"!a:x", "!b:x"}) {
		t.Errorf("RoomChain(cycle) = %v, want each room once", chain)
	}
}

// Expanding a search scope does not duplicate a room two scopes share.
func TestExpandingAScopeKeepsEachRoomOnce(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomUpgrade(ctx, "!new:x", RoomUpgrade{Predecessor: "!old:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}

	got, err := cache.RoomChains(ctx, []domain.RoomID{"!new:x", "!old:x", "!other:x"})
	if err != nil {
		t.Fatalf("RoomChains() error = %v", err)
	}
	if !sameRooms(got, []domain.RoomID{"!new:x", "!old:x", "!other:x"}) {
		t.Errorf("RoomChains() = %v, want each room once in caller order", got)
	}

	// Nothing to expand.
	if got, err := cache.RoomChains(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("RoomChains(nil) = %v, %v; want empty", got, err)
	}
}

func sameRooms(got, want []domain.RoomID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
