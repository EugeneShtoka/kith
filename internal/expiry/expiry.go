// Package expiry is what every network with disappearing messages shares: deleting each,
// as a deletion is, once its timer has run (the cache keeps the timers and countdowns),
// and telling clients.
package expiry

import (
	"context"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every is how often due messages are looked for.
const Every = 5 * time.Second

// Due is the cache's half: deleting what has run out by now, returning it as it was.
type Due interface {
	ExpireDue(ctx context.Context, owner domain.RoomOwner, now time.Time, keep bool) ([]domain.Message, error)
}

// Sweeper deletes one network's disappearing messages as they come due.
type Sweeper struct {
	Cache Due
	// Rooms are the network's rooms (domain.NetworkRooms): a message expires whether or
	// not its account is connected.
	Rooms domain.RoomOwner
	// Keep is [display.deleted] keep: an expired message keeps what it said, as a
	// deleted one does.
	Keep func() bool
	// Gone tells clients of each, as of a deletion; Changed is each room touched, once.
	Gone    func(stub domain.Message)
	Changed func(room domain.RoomID)
	Log     *slog.Logger
}

// Run sweeps now and every Every, until ctx ends.
func (s Sweeper) Run(ctx context.Context) {
	tick := time.NewTicker(Every)
	defer tick.Stop()
	for {
		s.Sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Sweep deletes what is due by now.
func (s Sweeper) Sweep(ctx context.Context, now time.Time) {
	keep := s.Keep()
	gone, err := s.Cache.ExpireDue(ctx, s.Rooms, now, keep)
	if err != nil {
		s.Log.Warn("deleting disappeared messages failed", "err", err)
	}
	rooms := map[domain.RoomID]bool{}
	for i := range gone {
		msg := &gone[i]
		stub := domain.Message{ID: msg.ID, RoomID: msg.RoomID, Redacted: true, RedactedAt: msg.RedactedAt}
		if keep {
			stub.Body, stub.Format, stub.Media = msg.Body, msg.Format, msg.Media
		}
		s.Gone(stub)
		rooms[msg.RoomID] = true
	}
	for room := range rooms {
		s.Changed(room)
	}
}
