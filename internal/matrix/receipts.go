package matrix

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// heldReceipt is a receipt whose event was not cached: Kind is a thread's root, or
// heldUnthreaded / heldMain for the room's own.
type heldReceipt struct{ Kind, Event domain.EventID }

// heldReceipts are our receipts whose event is not cached yet, stamped with their sync
// generation. Sync listeners run before their response's events, so settleReceipts
// retries them on ingest and fetchHeld asks the server about those held a sync later.
type heldReceipts struct {
	mu     sync.Mutex
	byRoom map[domain.RoomID]map[heldReceipt]int
	gen    int
}

// heldJob is one receipt still held from an earlier sync.
type heldJob struct {
	room domain.RoomID
	held heldReceipt
}

// hold keeps a receipt whose event has not arrived; all are kept, since which is
// newest is unknown.
func (r *heldReceipts) hold(roomID domain.RoomID, kind, eventID domain.EventID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byRoom == nil {
		r.byRoom = make(map[domain.RoomID]map[heldReceipt]int)
	}
	if r.byRoom[roomID] == nil {
		r.byRoom[roomID] = make(map[heldReceipt]int, 1)
	}
	r.byRoom[roomID][heldReceipt{Kind: kind, Event: eventID}] = r.gen
}

// drop forgets one held receipt, once placed.
func (r *heldReceipts) drop(roomID domain.RoomID, h heldReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRoom[roomID], h)
	if len(r.byRoom[roomID]) == 0 {
		delete(r.byRoom, roomID)
	}
}

// in is a room's held receipts, copied out of the lock.
func (r *heldReceipts) in(roomID domain.RoomID) []heldReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]heldReceipt, 0, len(r.byRoom[roomID]))
	for h := range r.byRoom[roomID] {
		out = append(out, h)
	}
	return out
}

// nextSync starts a sync generation and returns what is still held from earlier ones.
func (r *heldReceipts) nextSync() []heldJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	var jobs []heldJob
	for roomID, held := range r.byRoom {
		for h, gen := range held {
			if gen < r.gen {
				jobs = append(jobs, heldJob{room: roomID, held: h})
			}
		}
	}
	return jobs
}

// receiptFetches asks the server about each held receipt's event once, a few at a
// time; Stop joins them through wg.
type receiptFetches struct {
	mu    sync.Mutex
	tried map[fetchKey]bool
	sem   chan struct{}
	wg    sync.WaitGroup
}

// first marks an event as asked for, reporting whether it is new; if so, the fetch
// must hold a slot of the returned semaphore.
func (f *receiptFetches) first(key fetchKey) (slots chan struct{}, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tried[key] {
		return nil, false
	}
	if f.tried == nil {
		f.tried = make(map[fetchKey]bool)
		f.sem = make(chan struct{}, readFetchWorkers)
	}
	f.tried[key] = true
	return f.sem, true
}
