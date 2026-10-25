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
		{"newest first", mx + "!c:x"},
		{"oldest first", mx + "!b:x"},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			cache, ctx := openTemp(t), context.Background()

			links := map[domain.RoomID]domain.RoomID{mx + "!b:x": mx + "!a:x", mx + "!c:x": mx + "!b:x"}
			order2 := []domain.RoomID{mx + "!c:x", mx + "!b:x"}
			if order.first == mx+"!b:x" {
				order2 = []domain.RoomID{mx + "!b:x", mx + "!c:x"}
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
			up, known, err := cache.RoomUpgrade(ctx, mx+"!b:x")
			if err != nil || !known {
				t.Fatalf("RoomUpgrade(!b:x) = known %v, err %v; want known", known, err)
			}
			if up.Predecessor != mx+"!a:x" {
				t.Errorf("!b:x predecessor = %q, want !a:x — the second write blanked the first", up.Predecessor)
			}
			if up.Replacement != mx+"!c:x" {
				t.Errorf("!b:x replacement = %q, want !c:x", up.Replacement)
			}

			chain, err := cache.RoomChain(ctx, mx+"!c:x")
			if err != nil {
				t.Fatalf("RoomChain() error = %v", err)
			}
			want := []domain.RoomID{mx + "!c:x", mx + "!b:x", mx + "!a:x"}
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

	if _, known, err := cache.RoomUpgrade(ctx, mx+"!plain:x"); err != nil || known {
		t.Fatalf("unlinked room = known %v, err %v; want unknown", known, err)
	}
	chain, err := cache.RoomChain(ctx, mx+"!plain:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if !sameRooms(chain, []domain.RoomID{mx + "!plain:x"}) {
		t.Errorf("RoomChain(unlinked) = %v, want just itself", chain)
	}
}

// Two rooms naming each other as predecessor form a cycle; the walk stops at rooms it
// has seen, not only at the hop limit.
func TestACycleInTheCacheDoesNotSpin(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomUpgrade(ctx, mx+"!a:x", RoomUpgrade{Predecessor: mx + "!b:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}
	if err := cache.SaveRoomUpgrade(ctx, mx+"!b:x", RoomUpgrade{Predecessor: mx + "!a:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}

	chain, err := cache.RoomChain(ctx, mx+"!a:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if !sameRooms(chain, []domain.RoomID{mx + "!a:x", mx + "!b:x"}) {
		t.Errorf("RoomChain(cycle) = %v, want each room once", chain)
	}
}

// Expanding a search scope does not duplicate a room two scopes share.
func TestExpandingAScopeKeepsEachRoomOnce(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()

	if err := cache.SaveRoomUpgrade(ctx, mx+"!new:x", RoomUpgrade{Predecessor: mx + "!old:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade() error = %v", err)
	}

	got, err := cache.RoomChains(ctx, []domain.RoomID{mx + "!new:x", mx + "!old:x", mx + "!other:x"})
	if err != nil {
		t.Fatalf("RoomChains() error = %v", err)
	}
	if !sameRooms(got, []domain.RoomID{mx + "!new:x", mx + "!old:x", mx + "!other:x"}) {
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
