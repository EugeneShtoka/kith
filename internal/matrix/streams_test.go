package matrix

import (
	"context"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Regression: streams are also written outside the sync loop (recount from RPC,
// mautrix goroutines), so a send racing close panicked.
func TestEmitDuringCloseStreamsDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Repeated: the interleaving is what is tested.
	for range 200 {
		b := New(nil)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Go(func() {
				<-start
				emit(&b.out, b.out.msgs, domain.Message{ID: "$e:x"})
				emit(&b.out, b.out.unread, domain.Unread{RoomID: "!r:x"})
				emit(&b.out, b.out.reactions, domain.ReactionUpdate{Reaction: domain.Reaction{RoomID: "!r:x"}})
				emit(&b.out, b.out.activity, domain.Activity{RoomID: "!r:x"})
				emit(&b.out, b.out.invites, []domain.Room{{ID: "!r:x"}})
			})
		}
		wg.Go(func() {
			<-start
			b.out.close()
		})

		close(start) // release them together
		wg.Wait()
	}
}

func TestCloseStreamsIsIdempotent(t *testing.T) {
	t.Parallel()

	b := New(nil)
	b.out.close()
	b.out.close()

	// A closed stream reports ok=false.
	if _, ok := <-b.Messages(); ok {
		t.Error("Messages() still open after the streams closed; listen commands would block")
	}
	if _, ok := <-b.Unread(); ok {
		t.Error("Unread() still open after the streams closed")
	}
}

// Deterministic half: recount from an RPC handler after the streams closed.
func TestRecountAfterCloseStreamsDoesNotPanic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := testCache(t)

	b := New(cache)
	client, err := mautrix.NewClient("https://example.org", id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b.client = client

	// recount only sends on a change, so seed a non-zero last-known state.
	b.unread.seed([]domain.Unread{{RoomID: "!r:x", Messages: 5}}, nil)

	b.out.close()
	b.recount(ctx, "!r:x") // the RPC handler's path, after shutdown
}
