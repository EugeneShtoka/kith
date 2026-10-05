package route

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
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

// Invites streams every network's invite sets; with no network that has invites, it
// carries nothing and closes when the router stops.
func (r *Router) Invites() <-chan []domain.Room {
	r.openStreams()
	return r.invites
}

// Verifications streams device verifications; with no network that verifies, it
// carries nothing and closes when the router stops.
func (r *Router) Verifications() <-chan domain.Verification {
	r.openStreams()
	return r.verifications
}

// openStreams builds the merged streams once. With one adapter they are its own
// channels: nothing to merge. A role's streams are every adapter's with the role,
// logged in or not.
func (r *Router) openStreams() {
	r.streamsOnce.Do(func() {
		r.messages = merge(r.done, r.all, Adapter.Messages)
		r.activity = merge(r.done, r.all, Adapter.Activity)
		r.unread = merge(r.done, r.all, Adapter.Unread)
		r.reactions = merge(r.done, r.all, Adapter.Reactions)
		r.invites, r.verifications = r.noInvites, r.noVerifications
		if members := withRole[api.Membership](r.all); len(members) > 0 {
			r.invites = merge(r.done, members, api.Membership.Invites)
		}
		if verifiers := withRole[api.Verification](r.all); len(verifiers) > 0 {
			r.verifications = merge(r.done, verifiers, api.Verification.Verifications)
		}
	})
}

// withRole is the adapters with role C, logged in or not.
func withRole[C any](adapters []Adapter) []C {
	var out []C
	for _, a := range adapters {
		if c, ok := a.(C); ok {
			out = append(out, c)
		}
	}
	return out
}

// merge is one stream carrying every adapter's: it closes when all of theirs have
// closed, or when done does. An adapter's order is kept; across adapters there is
// none to keep.
func merge[A, T any](done <-chan struct{}, adapters []A, stream func(A) <-chan T) <-chan T {
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
