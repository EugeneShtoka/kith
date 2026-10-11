package notify

import (
	"sync"
	"time"
)

// Limit caps how many popups one room may raise per Window.
type Limit struct {
	Max    int
	Window time.Duration
}

// On reports whether the limit does anything.
func (l Limit) On() bool { return l.Max > 0 && l.Window > 0 }

// Decision is what to do with a notification the rules already approved.
type Decision int

const (
	// Deliver posts it as is: the room is within its burst.
	Deliver Decision = iota
	// Summarize posts one popup with the count of held messages instead.
	Summarize
	// Hold posts nothing.
	Hold
)

// Limiter holds the per-room burst state.
type Limiter struct {
	mu    sync.Mutex
	rooms map[string]*window
}

// window is one room's burst state.
type window struct {
	started    time.Time
	delivered  int
	held       int
	summarized time.Time
}

// Allow decides what to do with an approved notification from room, and returns the
// held count for a summary.
func (l *Limiter) Allow(room string, limit Limit, now time.Time) (Decision, int) {
	if !limit.On() {
		return Deliver, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rooms == nil {
		l.rooms = make(map[string]*window, 8)
	}

	w, ok := l.rooms[room]
	if !ok || now.Sub(w.started) >= limit.Window {
		// A fresh window.
		if ok && w.held > 0 {
			held := w.held + 1
			l.rooms[room] = &window{started: now, delivered: 1, summarized: now}
			return Summarize, held
		}
		l.rooms[room] = &window{started: now, delivered: 1}
		return Deliver, 0
	}

	if w.delivered < limit.Max {
		w.delivered++
		return Deliver, 0
	}

	// Over the burst.
	w.held++
	if w.summarized.IsZero() || now.Sub(w.summarized) >= limit.Window {
		w.summarized = now
		held := w.held
		w.held = 0
		return Summarize, held
	}
	return Hold, 0
}
