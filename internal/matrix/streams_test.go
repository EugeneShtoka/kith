package matrix

import (
	"context"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Streams are written outside the sync loop too (RPC recounts, mautrix goroutines),
// so a send racing close must not panic.
func TestEmitDuringCloseStreamsDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Repeated: the interleaving is what is tested.
	for range 200 {
		b := newBackend(nil)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Go(func() {
				<-start
				emit(&b.out, b.out.msgs, domain.Message{ID: mx + "$e:x"})
				emit(&b.out, b.out.unread, domain.Unread{RoomID: mx + "!r:x"})
				emit(&b.out, b.out.reactions, domain.ReactionUpdate{Reaction: domain.Reaction{RoomID: mx + "!r:x"}})
				emit(&b.out, b.out.activity, domain.Activity{RoomID: mx + "!r:x"})
				emit(&b.out, b.out.invites, []domain.Room{{ID: mx + "!r:x"}})
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

	b := newBackend(nil)
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

// A recount from an RPC handler after the streams closed does not panic.
func TestRecountAfterCloseStreamsDoesNotPanic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := testCache(t)

	b := newBackend(cache)
	client, err := mautrix.NewClient("https://example.org", id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b.client = client

	// recount only sends on a change, so seed a non-zero last-known state.
	b.unread.seed([]domain.Unread{{RoomID: mx + "!r:x", Messages: 5}}, nil)

	b.out.close()
	b.recount(ctx, mx+"!r:x") // the RPC handler's path, after shutdown
}
