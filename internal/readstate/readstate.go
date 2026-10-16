// Package readstate is what every network's local-first reading shares: a room is read
// here before the network hears of it, the mark the network has not taken waits in
// Owed until it does, and a read that cannot be kept here is an error that reads
// nothing and owes nothing.
package readstate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Owed is the read marks a network has not taken yet, by room, each at its position P:
// a mark taken settles only what it covered, never a newer one owed meanwhile.
type Owed[P any] struct {
	// after reports whether position a is later than b.
	after func(a, b P) bool

	mu    sync.Mutex
	marks map[domain.RoomID]P
}

// NewOwed is an empty queue ordering positions by after.
func NewOwed[P any](after func(a, b P) bool) *Owed[P] {
	return &Owed[P]{after: after, marks: map[domain.RoomID]P{}}
}

// Add owes room read through p, unless a later mark is owed already.
func (o *Owed[P]) Add(room domain.RoomID, p P) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.marks[room]; !ok || !o.after(old, p) {
		o.marks[room] = p
	}
}

// Take removes and returns the marks owed for the rooms of account (the account part
// of their IDs).
func (o *Owed[P]) Take(account string) map[domain.RoomID]P {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[domain.RoomID]P{}
	for room, p := range o.marks {
		if domain.ParseID(string(room)).Account == account {
			out[room] = p
			delete(o.marks, room)
		}
	}
	return out
}

// Settle drops room's mark once the network took p, unless a later one was owed
// meanwhile.
func (o *Owed[P]) Settle(room domain.RoomID, p P) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if old, ok := o.marks[room]; ok && !o.after(old, p) {
		delete(o.marks, room)
	}
}

// Has reports whether a mark is owed for room.
func (o *Owed[P]) Has(room domain.RoomID) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.marks[room]
	return ok
}

// Messages is the cache's messages, as TimeOf reads them.
type Messages interface {
	MessageByID(ctx context.Context, room domain.RoomID, event domain.EventID) (domain.Message, bool, error)
}

// TimeOf is when a cached message was sent, the position a read through it stands
// at; fallback when it is not cached (or names no time, or there is no event). A
// lookup that fails is an error, never fallback: a default saved as a read position
// reads what was never shown.
func TimeOf(ctx context.Context, cache Messages, room domain.RoomID, event domain.EventID, fallback time.Time) (time.Time, error) {
	if event == "" {
		return fallback, nil
	}
	msg, ok, err := cache.MessageByID(ctx, room, event)
	if err != nil {
		return time.Time{}, fmt.Errorf("read position of %s: %w", event, err)
	}
	if !ok || msg.Timestamp.IsZero() {
		return fallback, nil
	}
	return msg.Timestamp, nil
}

// Latest is the cache's newest message of each room, as MarkRoomsRead reads it.
type Latest interface {
	LatestEvents(ctx context.Context, rooms []domain.RoomID) (map[domain.RoomID]domain.EventID, error)
}

// MarkRoomsRead reads each room through its newest cached message with markRead: a
// room with none is skipped, one markRead refuses is failed (the first refusal kept),
// and only the rest count as marked.
func MarkRoomsRead(ctx context.Context, cache Latest, rooms []domain.RoomID, markRead func(room domain.RoomID, event domain.EventID) error) (domain.ReadResult, error) {
	var result domain.ReadResult
	latest, err := cache.LatestEvents(ctx, rooms)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("newest messages: %w", err)
	}
	for _, room := range rooms {
		event, ok := latest[room]
		if !ok {
			result.Skipped++
			continue
		}
		if err := markRead(room, event); err != nil {
			result.Failed++
			if result.FirstError == "" {
				result.FirstError = err.Error()
			}
			continue
		}
		result.Marked++
	}
	return result, nil
}
