package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hubSettle bounds every wait here. The hub is pure in-memory fan-out, so
// anything approaching this is a deadlock.
const hubSettle = 5 * time.Second

// recv takes one value, failing rather than hanging if none arrives.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed, want a value", what)
		}
		return v
	case <-time.After(hubSettle):
		t.Fatalf("%s: timed out", what)
		panic("unreachable")
	}
}

// Every subscriber sees every event.
func TestHubFansOutToEverySubscriber(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	_, first := h.subscribe()
	_, second := h.subscribe()

	h.broadcast(7)

	if got := recv(t, first, "first subscriber"); got != 7 {
		t.Errorf("first subscriber got %d, want 7", got)
	}
	if got := recv(t, second, "second subscriber"); got != 7 {
		t.Errorf("second subscriber got %d, want 7", got)
	}
}

// A subscriber that has stopped reading loses events instead of stalling the pump, and
// loses them alone.
func TestHubDropsOnlyForTheFullSubscriber(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	_, stalled := h.subscribe()
	_, draining := h.subscribe()

	// Interleaved rather than concurrent: the point is that a full queue never blocks the
	// broadcast, and reading one value per broadcast proves the draining subscriber missed
	// nothing without depending on goroutine timing.
	overflow := 3
	for i := range subBuffer + overflow {
		h.broadcast(i)
		if got := recv(t, draining, "draining subscriber"); got != i {
			t.Fatalf("draining subscriber got %d, want %d", got, i)
		}
	}

	if got := len(stalled); got != subBuffer {
		t.Errorf("stalled subscriber holds %d, want the buffer's %d", got, subBuffer)
	}
}

// Releasing closes the subscriber's channel — the signal a streaming handler's
// reader treats as a clean end — and forgets it: a broadcast sending to a closed
// channel would panic.
func TestHubReleaseClosesAndForgets(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	id, ch := h.subscribe()
	h.release(id)
	h.release(id) // idempotent: a handler may unwind more than once

	if _, ok := <-ch; ok {
		t.Error("channel open after release, want closed")
	}
	for range subBuffer + 1 {
		h.broadcast(1)
	}
}

// The source closing ends every subscriber, so each attached client sees the same
// clean end an in-process reader saw when the sync loop stopped.
func TestHubPumpEndsSubscribersWhenSourceCloses(t *testing.T) {
	t.Parallel()

	src := make(chan int, 1)
	h := newHub[int]()
	_, ch := h.subscribe()

	done := make(chan struct{})
	go func() { defer close(done); h.pump(context.Background(), src) }()

	src <- 42
	if got := recv(t, ch, "subscriber"); got != 42 {
		t.Errorf("subscriber got %d, want 42", got)
	}
	close(src)

	select {
	case <-done:
	case <-time.After(hubSettle):
		t.Fatal("pump did not return after the source closed")
	}
	if _, ok := <-ch; ok {
		t.Error("subscriber channel open after the source closed, want closed")
	}
}

// A canceled context ends the hub too.
func TestHubPumpEndsOnContextCancel(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	_, ch := h.subscribe()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); h.pump(ctx, make(chan int)) }()
	cancel()

	select {
	case <-done:
	case <-time.After(hubSettle):
		t.Fatal("pump did not return after cancellation")
	}
	if _, ok := <-ch; ok {
		t.Error("subscriber channel open after cancellation, want closed")
	}
}

// Subscribing after the source ended hands back a closed channel rather than one nothing
// will ever feed or close.
func TestHubSubscribeAfterEnd(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	h.end()

	_, ch := h.subscribe()
	if _, ok := <-ch; ok {
		t.Error("channel open when subscribing after the source ended, want closed")
	}
	if got := h.subscribers(); got != 0 {
		t.Errorf("subscribers() = %d, want 0", got)
	}
}

// The notifier's delivery can be slow, and a burst after a suspend is hundreds of
// messages: its queue holds them, where a client's would drop the rest.
func TestTheNotifierQueueHoldsABurst(t *testing.T) {
	t.Parallel()
	s := NewStreams()
	_, msgs := s.SubscribeMessages()
	for i := range 1000 {
		s.messages.broadcast(domain.Message{ID: domain.EventID(fmt.Sprint(i))})
	}
	if got := len(msgs); got != 1000 {
		t.Errorf("the notifier holds %d of a 1000-message burst, want all", got)
	}
}
