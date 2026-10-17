package signalhelper

import (
	"cmp"
	"context"
	"slices"
	"sync"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// Sink is where Network hands kithd an account's events.
type Sink interface {
	// Deliver hands kithd event and waits until kithd has kept it, with no timeout of
	// its own: Signal is told an event arrived only once it returns, so a helper that
	// kithd is gone from exits instead, with the event unacknowledged.
	Deliver(event *signalv1.EventsResponse)
	// Tell hands kithd event, which waits for nothing (a connection's state).
	Tell(event *signalv1.EventsResponse)
}

// outbox is the events on their way to kithd: each with a token waits in it until
// kithd acks it, and a new listener is sent every one still waiting first.
type outbox struct {
	mu   sync.Mutex
	next uint64
	// waiting is every event sent or to be sent and not yet acked, by token; queue is
	// what the listener has not been sent yet, in order.
	waiting map[uint64]*waiting
	queue   []*signalv1.EventsResponse
	// changed is closed (and replaced) when the queue grows or a listener takes over,
	// waking every listener; listener counts them, so an older one steps aside.
	changed  chan struct{}
	listener uint64
}

type waiting struct {
	event *signalv1.EventsResponse
	acked chan struct{}
}

func newOutbox() *outbox {
	return &outbox{waiting: map[uint64]*waiting{}, changed: make(chan struct{})}
}

// Deliver queues event with a token and waits until it is acked.
func (o *outbox) Deliver(event *signalv1.EventsResponse) {
	o.mu.Lock()
	o.next++
	event.Token = o.next
	w := &waiting{event: event, acked: make(chan struct{})}
	o.waiting[event.GetToken()] = w
	o.queue = append(o.queue, event)
	o.wakeLocked()
	o.mu.Unlock()
	<-w.acked
}

// Tell queues event without a token.
func (o *outbox) Tell(event *signalv1.EventsResponse) {
	event.Token = 0
	o.mu.Lock()
	o.queue = append(o.queue, event)
	o.wakeLocked()
	o.mu.Unlock()
}

// wakeLocked wakes every listener; o.mu is held.
func (o *outbox) wakeLocked() {
	close(o.changed)
	o.changed = make(chan struct{})
}

// ack releases the event with token; an unknown token (acked already) is nothing.
func (o *outbox) ack(token uint64) {
	o.mu.Lock()
	w := o.waiting[token]
	delete(o.waiting, token)
	o.mu.Unlock()
	if w != nil {
		close(w.acked)
	}
}

// listen sends events with send until ctx ends, send fails, or a newer listener takes
// over. It starts with every event still waiting, in order, then what comes.
func (o *outbox) listen(ctx context.Context, send func(*signalv1.EventsResponse) error) error {
	o.mu.Lock()
	o.listener++
	me := o.listener
	// Unacked events a former listener was sent may never have been kept: send them
	// again, ahead of the rest. Tokenless ones (states) are not worth repeating.
	var again []*signalv1.EventsResponse
	for _, w := range o.waiting {
		again = append(again, w.event)
	}
	queued := make(map[uint64]bool, len(o.queue))
	for _, e := range o.queue {
		if e.GetToken() != 0 {
			queued[e.GetToken()] = true
		}
	}
	var resend []*signalv1.EventsResponse
	for _, e := range again {
		if !queued[e.GetToken()] {
			resend = append(resend, e)
		}
	}
	slices.SortFunc(resend, func(a, b *signalv1.EventsResponse) int { return cmp.Compare(a.GetToken(), b.GetToken()) })
	o.queue = append(resend, o.queue...)
	o.wakeLocked() // an older listener steps aside
	o.mu.Unlock()
	for {
		o.mu.Lock()
		if o.listener != me {
			o.mu.Unlock()
			return nil
		}
		var next *signalv1.EventsResponse
		if len(o.queue) > 0 {
			next, o.queue = o.queue[0], o.queue[1:]
		}
		changed := o.changed
		o.mu.Unlock()
		if next == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-changed:
			}
			continue
		}
		if err := send(next); err != nil {
			return err
		}
	}
}
