package matrix

import (
	"context"
	"errors"
	"sync"
)

// errRewind ends a sync loop so Start can restart it from a full initial sync: the
// loop keeps its token in memory and saves it after every response, so rewinding
// the store under a running loop does nothing, and races its save.
var errRewind = errors.New("matrix: sync rewound")

// syncRewind orders a rewind (ClearCache) against the sync loop. While the loop runs,
// a rewind is left pending and taken by its next response (see failingSyncer); while
// it does not, the rewind is done at once, and Start waits for it.
type syncRewind struct {
	mu      sync.Mutex
	syncing bool
	pending bool
	// owed: the next Start must rewind. Decided when the backend is built, from the
	// cache as it was opened (rebuilt, or holding no room), and set by RewindSync;
	// not from the cache when Start runs, which a room refresh may have filled by then.
	owed bool
}

// owe marks a rewind due at the next Start.
func (r *syncRewind) owe() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owed = true
}

// isOwed reports whether a rewind is due at the next Start.
func (r *syncRewind) isOwed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owed
}

// paid records a rewind done.
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

// take reports a pending rewind and clears it; the sync goroutine asks per response.
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
		// A loop that ended for another reason leaves the durable rule to rewind:
		// the cache is empty, so the next start does (see resyncEmptiedCache).
		r.pending = false
	}
}

// syncUntilDone runs the sync loop, restarting it from an empty token after each
// rewind. The loop is stopped when it returns, so nothing races the reset.
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
