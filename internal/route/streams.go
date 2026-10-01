package route

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer is each merged stream's buffer, as large as one adapter's: the merge
// adds no loss of its own while its reader keeps up.
const streamBuffer = 64

// Messages is every network's live messages.
func (r *Router) Messages() <-chan domain.Message {
	r.openStreams()
	return r.messages
}

// Activity is every network's typing and read positions.
func (r *Router) Activity() <-chan domain.Activity {
	r.openStreams()
	return r.activity
}

// Unread is every network's unread changes.
func (r *Router) Unread() <-chan domain.Unread {
	r.openStreams()
	return r.unread
}

// Reactions is every network's reaction changes.
func (r *Router) Reactions() <-chan domain.ReactionUpdate {
	r.openStreams()
	return r.reactions
}

// openStreams builds the merged streams once. With one adapter they are its own
// channels: nothing to merge.
func (r *Router) openStreams() {
	r.streamsOnce.Do(func() {
		r.messages = merge(r.done, r.all, Adapter.Messages)
		r.activity = merge(r.done, r.all, Adapter.Activity)
		r.unread = merge(r.done, r.all, Adapter.Unread)
		r.reactions = merge(r.done, r.all, Adapter.Reactions)
	})
}

// merge is one stream carrying every adapter's: it closes when all of theirs have
// closed, or when done does. An adapter's order is kept; across adapters there is
// none to keep.
func merge[T any](done <-chan struct{}, adapters []Adapter, stream func(Adapter) <-chan T) <-chan T {
	if len(adapters) == 1 {
		return stream(adapters[0])
	}
	out := make(chan T, streamBuffer)
	var wg sync.WaitGroup
	for _, a := range adapters {
		in := stream(a)
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				case v, ok := <-in:
					if !ok {
						return
					}
					select {
					case out <- v:
					case <-done:
						return
					}
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
