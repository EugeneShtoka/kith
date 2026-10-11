package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// refreshDebounce coalesces a burst of room/space changes (e.g. m.space.child events)
// into one refresh.
const refreshDebounce = 2 * time.Second

type refreshBackend interface {
	RefreshRooms(ctx context.Context) ([]domain.Room, error)
	RefreshSpaces(ctx context.Context) ([]domain.Space, error)
}

// Refresher re-reads the room list and space hierarchy when sync reports a change
// (/sync never writes those caches); after runs once a refresh has landed.
type Refresher struct {
	b     refreshBackend
	after func()
	// signal is a depth-1 flag written without blocking from the sync goroutine.
	signal chan struct{}
	// log hears failed refreshes; nil is silent.
	log *slog.Logger
}

// NewRefresher returns a Refresher over b.
func NewRefresher(b refreshBackend, after func()) *Refresher {
	return &Refresher{b: b, after: after, signal: make(chan struct{}, 1)}
}

// Changed flags that rooms or spaces changed. It never blocks.
func (r *Refresher) Changed() {
	select {
	case r.signal <- struct{}{}:
	default: // already flagged
	}
}

// Run refreshes at startup (a cold cache would serve an empty room list) and then on
// each debounced signal, until ctx ends. It blocks.
func (r *Refresher) Run(ctx context.Context) {
	r.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.signal:
			if !sleepUntil(ctx, refreshDebounce) {
				return
			}
			// Drain what arrived while waiting: this refresh covers it.
			select {
			case <-r.signal:
			default:
			}
			r.refresh(ctx)
		}
	}
}

// refresh re-reads both caches; a failure waits for the next change. Spaces first:
// the room list is sifted against the hierarchy.
func (r *Refresher) refresh(ctx context.Context) {
	roomsOK := true
	if _, err := r.b.RefreshSpaces(ctx); err != nil {
		roomsOK = false
		r.warn(ctx, "refresh spaces", err)
	}
	if _, err := r.b.RefreshRooms(ctx); err != nil {
		roomsOK = false
		r.warn(ctx, "refresh rooms", err)
	}
	if roomsOK && r.after != nil {
		r.after()
	}
}

// UseLogger sets where a failed refresh is logged. Call before Run.
func (r *Refresher) UseLogger(log *slog.Logger) { r.log = log }

func (r *Refresher) warn(ctx context.Context, op string, err error) {
	if r.log != nil {
		r.log.Log(ctx, levelFor(ctx), op+" failed; retrying on the next change", "op", op, "err", err)
	}
}

// sleepUntil waits for d, reporting false if ctx ended first.
func sleepUntil(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
