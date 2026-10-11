package route

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer matches one adapter's buffer, so merging adds no loss while the reader
// keeps up.
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

// Invites streams every network's invite sets; with no such network it carries nothing
// and closes on Stop.
func (r *Router) Invites() <-chan []domain.Room {
	r.openStreams()
	return r.invites
}

// Verifications streams device verifications; with no such network it carries nothing
// and closes on Stop.
func (r *Router) Verifications() <-chan domain.Verification {
	r.openStreams()
	return r.verifications
}

// openStreams builds the merged streams once; with one adapter they are its own
// channels. Role streams include adapters not logged in.
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

// merge carries every adapter's stream, closing when all have closed or done does.
// Order is kept per adapter only.
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
