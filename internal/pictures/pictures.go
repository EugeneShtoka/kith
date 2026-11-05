// Package pictures is what every network's pictures share: given what each room's (or
// person's) picture is now, the cache's copy (db.SetPicture) is kept current, the
// network asked only for what changed or is due, one sweep at a time per connection.
package pictures

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Store is where pictures are kept, by K, a room's ID or a person's: the cache.
type Store[K ~string] interface {
	PictureChecks(ctx context.Context) (map[K]domain.PictureCheck, error)
	SetPicture(ctx context.Context, key K, id string, data []byte, at time.Time) error
	PictureUnchanged(ctx context.Context, key K, at time.Time) error
}

// Want is one room's picture as its network has it now, in one of two shapes.
type Want struct {
	// Version names the picture where the network says it unasked (a listing names
	// it), "" for none; Fetch then downloads it, called only when the kept one differs.
	Version string
	Fetch   func(ctx context.Context) ([]byte, error)
	// Ask, instead, learns the picture where only asking tells: given the version kept
	// ("" none), its version now and, when that differs, its bytes. It is called when
	// the room was never asked, or Every has passed since; Every zero asks every sweep.
	Ask   func(ctx context.Context, kept string) (version string, data []byte, err error)
	Every time.Duration
}

// ErrRefused marks a picture its network refuses on its own: the sweep goes on without
// it, and the next one asks again.
var ErrRefused = errors.New("the network refused this picture")

// Sweep keeps each room's picture in wants current in store at now, in room order. It
// stops at the first failure but a refusal; what is left is tried with the next sweep.
// changed is whether any room's kept picture changed.
func Sweep[K ~string](ctx context.Context, store Store[K], wants map[K]Want, now time.Time) (changed bool, err error) {
	checks, err := store.PictureChecks(ctx)
	if err != nil {
		return false, err //nolint:wrapcheck // the caller names its network
	}
	rooms := make([]K, 0, len(wants))
	for room := range wants {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	for _, room := range rooms {
		got, err := keep(ctx, store, room, wants[room], checks, now)
		changed = changed || got
		if errors.Is(err, ErrRefused) {
			continue
		}
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// keep keeps one room's picture; changed is whether the kept one changed.
func keep[K ~string](ctx context.Context, store Store[K], room K, w Want, checks map[K]domain.PictureCheck, now time.Time) (changed bool, err error) {
	check, asked := checks[room]
	var (
		version string
		data    []byte
	)
	switch {
	case w.Ask != nil:
		if asked && w.Every > 0 && now.Sub(check.Checked) < w.Every {
			return false, nil
		}
		if version, data, err = w.Ask(ctx, check.ID); err != nil {
			return false, err
		}
		if len(data) == 0 && version != check.ID {
			version = "" // a new version always comes with its bytes
		}
		if asked && version == check.ID {
			return false, store.PictureUnchanged(ctx, room, now) //nolint:wrapcheck // the caller names its network
		}
	case asked && check.ID == w.Version:
		return false, nil
	default:
		if version = w.Version; version != "" {
			if data, err = w.Fetch(ctx); err != nil {
				return false, err
			}
		}
	}
	if len(data) == 0 {
		version = "" // as the store keeps it
	}
	if err := store.SetPicture(ctx, room, version, data, now); err != nil {
		return false, err //nolint:wrapcheck // the caller names its network
	}
	return version != check.ID, nil
}

// Sweeper runs a connection's sweeps one at a time, off the caller: wants given while
// one runs are swept after it, only the latest. Changed is heard when a sweep changed
// a picture, Failed when one stopped; either may be nil.
type Sweeper[K ~string] struct {
	Store   Store[K]
	Changed func()
	Failed  func(error)

	mu      sync.Mutex
	running bool
	pending bool
	want    map[K]Want
}

// Later sweeps wants under ctx, after the sweep running now, if any.
func (s *Sweeper[K]) Later(ctx context.Context, wants map[K]Want) {
	s.mu.Lock()
	s.want, s.pending = wants, true
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go s.run(ctx)
}

func (s *Sweeper[K]) run(ctx context.Context) {
	for {
		s.mu.Lock()
		if !s.pending {
			s.running = false
			s.mu.Unlock()
			return
		}
		want := s.want
		s.want, s.pending = nil, false
		s.mu.Unlock()
		changed, err := Sweep(ctx, s.Store, want, time.Now())
		if changed && s.Changed != nil {
			s.Changed()
		}
		if err != nil && s.Failed != nil {
			s.Failed(err)
		}
	}
}
