package matrix

import (
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountInvites is each account's invite set, which the adapter publishes as one.
type accountInvites struct {
	mu sync.Mutex
	by map[*runner][]domain.Room
}

// forward carries r's streams into the adapter's until r has stopped, draining what r
// published before it did: a message cached and streamed is never lost on the way.
// Caller holds mu, with the adapter not closing, so Stop waits for these.
func (m *Adapter) forward(r *runner) {
	b := r.InProc
	carry(&m.forwards, b.out.msgs, r.done, func(v domain.Message) { emit(&m.out, m.out.msgs, v) })
	carry(&m.forwards, b.out.unread, r.done, func(v domain.Unread) { emit(&m.out, m.out.unread, v) })
	carry(&m.forwards, b.out.reactions, r.done, func(v domain.ReactionUpdate) { emit(&m.out, m.out.reactions, v) })
	carry(&m.forwards, b.out.activity, r.done, func(v domain.Activity) { emit(&m.out, m.out.activity, v) })
	carry(&m.forwards, b.out.invites, r.done, func(v []domain.Room) { m.setInvites(r, v) })
	carry(&m.forwards, b.ver.events, r.done, func(v domain.Verification) {
		m.mu.Lock()
		if v.TxnID != "" {
			m.txns[v.TxnID] = r
		}
		m.mu.Unlock()
		emit(&m.out, m.verifications, v)
	})
}

// carry sends what in carries until it closes or done does; on done, what in still
// holds is sent first.
func carry[T any](wg *sync.WaitGroup, in <-chan T, done <-chan struct{}, send func(T)) {
	wg.Go(func() {
		for {
			select {
			case v, ok := <-in:
				if !ok {
					return
				}
				send(v)
			case <-done:
				for {
					select {
					case v, ok := <-in:
						if !ok {
							return
						}
						send(v)
					default:
						return
					}
				}
			}
		}
	})
}

// setInvites takes r's whole invite set and publishes every account's.
func (m *Adapter) setInvites(r *runner, rooms []domain.Room) {
	m.invites.mu.Lock()
	m.invites.by[r] = rooms
	all := m.allInvites()
	m.invites.mu.Unlock()
	emit(&m.out, m.out.invites, all)
}

// dropInvites forgets a removed account's invites and publishes the others'.
func (m *Adapter) dropInvites(r *runner) {
	m.invites.mu.Lock()
	_, had := m.invites.by[r]
	delete(m.invites.by, r)
	all := m.allInvites()
	m.invites.mu.Unlock()
	if had {
		emit(&m.out, m.out.invites, all)
	}
}

// allInvites is every account's invites, sorted for display. Caller holds invites.mu.
func (m *Adapter) allInvites() []domain.Room {
	var all []domain.Room
	for _, rooms := range m.invites.by {
		all = append(all, rooms...)
	}
	domain.SortRooms(all)
	return slices.Clip(all)
}

// Messages streams every account's incoming messages.
func (m *Adapter) Messages() <-chan domain.Message { return m.out.msgs }

// Unread streams every account's unread changes.
func (m *Adapter) Unread() <-chan domain.Unread { return m.out.unread }

// Reactions streams every account's reaction changes.
func (m *Adapter) Reactions() <-chan domain.ReactionUpdate { return m.out.reactions }

// Activity streams every account's typing and read positions.
func (m *Adapter) Activity() <-chan domain.Activity { return m.out.activity }

// Invites streams every account's pending invitations, whole, on each change.
func (m *Adapter) Invites() <-chan []domain.Room { return m.out.invites }

// Verifications streams every account's device verification steps.
func (m *Adapter) Verifications() <-chan domain.Verification { return m.verifications }

// Attached never fires in-process.
func (m *Adapter) Attached() <-chan bool { return neverAttaches }
