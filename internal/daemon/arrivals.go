package daemon

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Messages a network caches without announcing them (a sync after an outage, a page of
// scrollback, a rebuild) never reach Deliver, so the filters judge them here: the cache
// names each room that gained messages and the oldest of them, and a worker rescans
// those rooms from there, moving rooms and marking senders as a rescan applied from the
// TUI does. Live messages are named too; Deliver has judged them by the time the worker
// runs, so a move it made is settled and not made twice.

// arrivalSettle is how long the worker lets arrivals gather before judging them: a
// sync writes in many small batches, and Deliver goes first on the live ones.
const arrivalSettle = 2 * time.Second

// Arrived queues room to be judged from since, keeping the oldest time asked. kithd
// hands it to the cache (db.Cache.OnArrived); it never blocks.
func (n *Notifications) Arrived(room domain.RoomID, since time.Time) {
	if n.filters == nil {
		return
	}
	n.arrivalsMu.Lock()
	if n.arrivals == nil {
		n.arrivals = map[domain.RoomID]time.Time{}
	}
	if had, ok := n.arrivals[room]; !ok || since.Before(had) {
		n.arrivals[room] = since
	}
	n.arrivalsMu.Unlock()
	select {
	case n.arrivalsWake <- struct{}{}:
	default:
	}
}

// RunArrivals judges the queued rooms until ctx ends. It blocks.
func (n *Notifications) RunArrivals(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.arrivalsWake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(arrivalSettle):
		}
		n.judgeArrivals(ctx)
	}
}

// judgeArrivals takes every queued room and rescans each from its own time.
func (n *Notifications) judgeArrivals(ctx context.Context) {
	n.arrivalsMu.Lock()
	rooms := n.arrivals
	n.arrivals = nil
	n.arrivalsMu.Unlock()
	for room, since := range rooms {
		if ctx.Err() != nil {
			return
		}
		facts := []domain.RoomFacts{n.scope.Facts(ctx, room)}
		if _, err := n.filters.rescan(ctx, facts, since, true, nil); err != nil {
			n.filters.warn(ctx, "filters: judge the messages that arrived failed", err, "room", room)
		}
		if _, err := n.filters.rescanPeople(ctx, facts, since, true, nil); err != nil {
			n.filters.warn(ctx, "filters: judge the senders that arrived failed", err, "room", room)
		}
	}
}
