package daemon

import (
	"context"
	"sync"
)

// subBuffer bounds one subscriber's queue, matching InProc's UI channel buffer.
const subBuffer = 64

// hub fans one backend channel out to every subscriber; a receive removes the value,
// so each backend channel is read exactly once, here.
//
// Delivery is lossy per subscriber on purpose: a blocking send would let one stalled
// reader back up into the sync loop. A full subscriber recovers from the paired cache
// read (CachedUnread etc.).
type hub[T any] struct {
	mu   sync.Mutex
	subs map[uint64]chan T
	next uint64
	// ended means the source closed; a late subscriber gets a closed channel.
	ended bool
}

func newHub[T any]() *hub[T] {
	return &hub[T]{subs: make(map[uint64]chan T)}
}

// subscribe attaches a subscriber, returning its id (for release) and its channel.
func (h *hub[T]) subscribe() (uint64, <-chan T) { return h.subscribeWith(subBuffer) }

func (h *hub[T]) subscribeWith(buffer int) (uint64, <-chan T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan T, buffer)
	if h.ended {
		close(ch)
		return 0, ch
	}
	h.next++
	id := h.next
	h.subs[id] = ch
	return id, ch
}

// release detaches a subscriber and closes its channel. Idempotent.
func (h *hub[T]) release(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.subs[id]
	if !ok {
		return
	}
	delete(h.subs, id)
	close(ch)
}

// broadcast copies v to every subscriber, skipping full queues. The lock is held
// across the non-blocking sends so release cannot race a send.
func (h *hub[T]) broadcast(v T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- v:
		default: // a subscriber that stopped reading misses it
		}
	}
}

// end marks the source finished and closes every subscriber's channel.
func (h *hub[T]) end() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ended = true
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// pump copies src to the subscribers until src closes or ctx ends, then ends the hub.
// It blocks.
func (h *hub[T]) pump(ctx context.Context, src <-chan T) {
	defer h.end()
	for {
		select {
		case <-ctx.Done():
			return
		case v, ok := <-src:
			if !ok {
				return
			}
			h.broadcast(v)
		}
	}
}

func (h *hub[T]) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
