package matrix

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A count taken before a read position moved is not written over the moved state:
// storeCount refuses it, and the recount counts again.
func TestAStaleCountIsNotWrittenOverAMovedReadPosition(t *testing.T) {
	t.Parallel()
	srv := newEventServer(t, nil)
	b := readBackend(t, srv.Server)
	ctx := context.Background()
	b.advanceRead(ctx, readRoom, readPos{Event: "$old", TS: readAt(0).UnixMilli()})

	gen := b.unread.generation(readRoom)
	stale := b.countLocal(ctx, domain.Unread{RoomID: readRoom}) // counted at $old
	b.advanceRead(ctx, readRoom, readPos{Event: "$new", TS: readAt(10).UnixMilli()})
	if current := b.storeCount(readRoom, gen, stale, false); current {
		t.Fatal("a count of the older read position was written over the newer one")
	}
	if got := b.unread.get(readRoom); got.Messages != 0 {
		t.Fatalf("read through $new but the book counts %d unread", got.Messages)
	}
	b.recount(ctx, readRoom)
	if got := b.unread.get(readRoom); got.Messages != 0 {
		t.Fatalf("after a recount the book counts %d unread", got.Messages)
	}
}
