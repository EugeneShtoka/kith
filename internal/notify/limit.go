package notify

import (
	"sync"
	"time"
)

// What a room is allowed to interrupt you with, and what happens past it.

// Limit is how many popups one room may raise, over what period.
type Limit struct {
	Max    int
	Window time.Duration
}

// On reports whether the limit does anything.
func (l Limit) On() bool { return l.Max > 0 && l.Window > 0 }

// Decision is what to do with one notification that the rules have already approved.
type Decision int

const (
	// Deliver posts it as it stands: the room is still within its burst.
	Deliver Decision = iota
	// Summarize posts one popup naming how many messages arrived instead of this one.
	Summarize
	// Hold posts nothing.
	Hold
)

// Limiter is the per-room state behind that decision.
type Limiter struct {
	mu    sync.Mutex
	rooms map[string]*window
}

// window is one room's burst: when it started, how many popups it has raised, how many
// messages have been held since the last summary, and when that summary went out.
type window struct {
	started    time.Time
	delivered  int
	held       int
	summarized time.Time
}

// Allow decides what to do with one approved notification from a room.
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
