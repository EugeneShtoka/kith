package daemon_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// refreshCounter records what the Refresher asked the backend for.
type refreshCounter struct {
	mu     sync.Mutex
	rooms  int
	spaces int
	err    error
}

func (c *refreshCounter) RefreshRooms(context.Context) ([]domain.Room, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rooms++
	return nil, c.err
}

func (c *refreshCounter) RefreshSpaces(context.Context) ([]domain.Space, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spaces++
	return nil, c.err
}

func (c *refreshCounter) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rooms, c.spaces
}

// A cold cache looks like an empty account, so the daemon fills it at startup.
func TestRefresherRefreshesAtStartup(t *testing.T) {
	t.Parallel()

	backend := &refreshCounter{}
	var invalidated atomic64
	r := daemon.NewRefresher(backend, invalidated.inc)

	ctx := t.Context()
	go r.Run(ctx)

	waitFor(t, func() bool {
		rooms, spaces := backend.counts()
		return rooms == 1 && spaces == 1
	})
	waitFor(t, func() bool { return invalidated.get() == 1 })
}

// A reported change reaches the caches, then derived state is dropped.
func TestRefresherActsOnAChange(t *testing.T) {
	t.Parallel()

	backend := &refreshCounter{}
	var invalidated atomic64
	r := daemon.NewRefresher(backend, invalidated.inc)

	ctx := t.Context()
	go r.Run(ctx)
	waitFor(t, func() bool { return invalidated.get() == 1 }) // the startup refresh

	r.Changed()
	waitFor(t, func() bool {
		rooms, _ := backend.counts()
		return rooms == 2
	})
	waitFor(t, func() bool { return invalidated.get() == 2 })
}

// Changed is called from the sync goroutine, so it never blocks and signals coalesce.
func TestChangedNeverBlocksAndCoalesces(t *testing.T) {
	t.Parallel()

	backend := &refreshCounter{}
	r := daemon.NewRefresher(backend, nil)

	// Nothing drains the signal; this must still return promptly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			r.Changed()
		}
	}()
	select {
	case <-done:
	case <-time.After(settle):
		t.Fatal("Changed blocked; it is called from the sync goroutine and must not")
	}

	// A thousand signals are one refresh.
	ctx := t.Context()
	go r.Run(ctx)
	waitFor(t, func() bool {
		rooms, _ := backend.counts()
		return rooms >= 1
	})
	time.Sleep(200 * time.Millisecond)
	if rooms, _ := backend.counts(); rooms > 2 {
		t.Errorf("%d refreshes for one burst, want it coalesced", rooms)
	}
}

// A briefly unreachable homeserver is ordinary.
func TestRefresherSurvivesAFailure(t *testing.T) {
	t.Parallel()

	backend := &refreshCounter{err: errors.New("homeserver unreachable")}
	var invalidated atomic64
	r := daemon.NewRefresher(backend, invalidated.inc)

	ctx := t.Context()
	go r.Run(ctx)

	waitFor(t, func() bool {
		rooms, _ := backend.counts()
		return rooms == 1
	})
	time.Sleep(100 * time.Millisecond)
	if got := invalidated.get(); got != 0 {
		t.Errorf("invalidated %d times after a failed refresh, want 0", got)
	}

	// It keeps working once the homeserver is back.
	backend.mu.Lock()
	backend.err = nil
	backend.mu.Unlock()
	r.Changed()
	waitFor(t, func() bool { return invalidated.get() == 1 })
}

// atomic64 is a counter readable from another goroutine.
type atomic64 struct {
	mu sync.Mutex
	n  int
}

func (a *atomic64) inc() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
}

func (a *atomic64) get() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
