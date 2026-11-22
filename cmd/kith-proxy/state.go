package main

import (
	"context"
	"sync"
	"time"
)

const (
	// heldAfter is how long the bridge fails to answer before the proxy answers from
	// its store, and liveAfter how long it answers again before the proxy forwards
	// again: a Wi-Fi that drops for a few seconds costs nothing, an outage one switch
	// each way.
	heldAfter = 10 * time.Second
	liveAfter = 5 * time.Second
	// probeEvery is how often the bridge is asked whether it answers, and probeWait how
	// long one answer may take.
	probeEvery = time.Second
	probeWait  = 3 * time.Second
)

// state is whether the proxy forwards to the bridge (live) or answers from its store
// (held), with each switch ending the calls of the state it leaves: a client's stream
// forwarded ends as the bridge goes, one served from the store as it comes back, and
// each reopens it the other way.
type state struct {
	mu    sync.Mutex
	held  bool
	since time.Time     // when held began
	turn  chan struct{} // closed at the next switch
}

func newState(held bool, now time.Time) *state {
	return &state{held: held, since: now, turn: make(chan struct{})}
}

// now is whether the proxy is held, and a channel closed when it next switches.
func (s *state) now() (held bool, turn <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held, s.turn
}

// set switches the proxy to held or live at now; the same state again changes nothing.
func (s *state) set(held bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == held {
		return
	}
	s.held = held
	if held {
		s.since = now
	}
	close(s.turn)
	s.turn = make(chan struct{})
}

// within is ctx ended also when the state switches from the one turn belongs to.
func within(ctx context.Context, turn <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-turn:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// hysteresis decides held or live from the bridge's answers to probes: held once it
// has not answered for heldAfter, live once it has answered throughout liveAfter.
type hysteresis struct {
	lastOK   time.Time // the last answer
	okSince  time.Time // the first of the answers unbroken since; zero after a failure
	lastFail time.Time
}

// heard records a probe's outcome at now and returns whether the proxy should be held,
// given it is held now.
func (h *hysteresis) heard(ok bool, now time.Time, held bool) bool {
	if ok {
		h.lastOK = now
		if h.okSince.IsZero() {
			h.okSince = now
		}
	} else {
		h.lastFail = now
		h.okSince = time.Time{}
	}
	if held {
		return !ok || now.Sub(h.okSince) < liveAfter
	}
	return !ok && now.Sub(h.lastOK) >= heldAfter
}
