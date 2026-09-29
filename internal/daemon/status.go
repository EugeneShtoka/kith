package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State is the daemon's view of itself: whether it is ready (a sync has landed — a
// listening socket over a cold cache is not ready), when it last synced, and the
// last error. Safe for concurrent use.
type State struct {
	mu       sync.Mutex
	syncedAt time.Time
	lastErr  string
}

// NewState returns a State that is not ready: nothing has synced yet.
func NewState() *State { return &State{} }

// Synced records a sync response arriving at t, making the daemon ready and
// clearing the last error.
func (s *State) Synced(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncedAt = t
	s.lastErr = ""
}

// Failed records why the sync loop stopped and clears readiness: a dead sync loop
// over a warm cache would otherwise look healthy while serving a frozen account.
func (s *State) Failed(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncedAt = time.Time{}
	s.lastErr = err.Error()
}

// Snapshot reports readiness, the last sync time (zero before the first) and the
// last error (empty when there has been none).
func (s *State) Snapshot() (ready bool, syncedAt time.Time, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.syncedAt.IsZero(), s.syncedAt, s.lastErr
}

// SyncFault returns nil when err (from api.Backend.Start) is just our own
// shutdown, so a clean stop does not exit non-zero or clear readiness.
func SyncFault(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil //nolint:nilerr // discarding the error is the point: our own shutdown is not a fault
	}
	return err
}
