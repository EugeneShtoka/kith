package matrix

import (
	"context"
	"errors"
	"sync"
)

// errRewind ends a sync loop so Start restarts it from a full initial sync: the loop
// holds its token in memory, so rewinding the store under it does nothing and races its save.
var errRewind = errors.New("matrix: sync rewound")

// syncRewind orders a rewind (ClearCache) against the sync loop: pending for the
// running loop's next response (see failingSyncer), else done at once.
type syncRewind struct {
	mu      sync.Mutex
	syncing bool
	pending bool
	// owed: the next Start must rewind. Set from the cache as opened (a room refresh
	// may fill it before Start) and by RewindSync.
	owed bool
}

func (r *syncRewind) owe() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owed = true
}

func (r *syncRewind) isOwed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owed
}

func (r *syncRewind) paid() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owed = false
}

// request rewinds now when no loop runs, or leaves the rewind for the running one.
func (r *syncRewind) request(rewind func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncing {
		r.pending = true
		return nil
	}
	return rewind()
}

// take reports and clears a pending rewind; the sync goroutine asks per response.
func (r *syncRewind) take() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.pending
	r.pending = false
	return pending
}

// running marks the loop as started or ended.
func (r *syncRewind) running(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncing = on
	if !on {
		// Ended otherwise: the cache is empty, so the next start rewinds (resyncEmptiedCache).
		r.pending = false
	}
}

// syncUntilDone runs the sync loop, restarting it from an empty token after each
// rewind; the loop has stopped before the reset, so nothing races it.
func (b *InProc) syncUntilDone(ctx context.Context) error {
	b.rewind.running(true)
	defer b.rewind.running(false)
	for {
		err := b.client.SyncWithContext(ctx)
		if !errors.Is(err, errRewind) {
			return err //nolint:wrapcheck // Start wraps it
		}
		if rerr := b.resetSyncPosition(ctx); rerr != nil {
			return rerr
		}
		b.rewind.paid()
		b.log().Info("sync rewound to a full initial sync", "op", "sync")
	}
}
