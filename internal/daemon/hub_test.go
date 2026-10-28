package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hubSettle bounds every wait; the hub is in-memory, so reaching it means deadlock.
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

// A subscriber that stopped reading loses events alone, without stalling the pump.
func TestHubDropsOnlyForTheFullSubscriber(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	_, stalled := h.subscribeWith(subBuffer)
	_, draining := h.subscribeWith(subBuffer)

	// Interleaved, not concurrent: one read per broadcast proves the draining subscriber
	// missed nothing without depending on goroutine timing.
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

// Release closes the channel (a clean end to the reader) and forgets it: sending to a
// closed channel would panic.
func TestHubReleaseClosesAndForgets(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	id, ch := h.subscribeWith(subBuffer)
	h.release(id)
	h.release(id) // idempotent: a handler may unwind more than once

	if _, ok := <-ch; ok {
		t.Error("channel open after release, want closed")
	}
	for range subBuffer + 1 {
		h.broadcast(1)
	}
}

// The source closing ends every subscriber cleanly.
func TestHubPumpEndsSubscribersWhenSourceCloses(t *testing.T) {
	t.Parallel()

	src := make(chan int, 1)
	h := newHub[int]()
	_, ch := h.subscribeWith(subBuffer)

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

func TestHubPumpEndsOnContextCancel(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	_, ch := h.subscribeWith(subBuffer)
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

// Subscribing after the end returns a closed channel.
func TestHubSubscribeAfterEnd(t *testing.T) {
	t.Parallel()

	h := newHub[int]()
	h.end()

	_, ch := h.subscribeWith(subBuffer)
	if _, ok := <-ch; ok {
		t.Error("channel open when subscribing after the source ended, want closed")
	}
	if got := h.subscribers(); got != 0 {
		t.Errorf("subscribers() = %d, want 0", got)
	}
}

// The notifier's queue holds a post-suspend burst where a client's would drop.
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
