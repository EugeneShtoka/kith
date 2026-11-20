package daemon

import (
	"context"
	"crypto"
	"errors"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The pace of drafts into the mesh: a place's draft is written once saves here have
// rested draftsIdle, or at once when no window has its room open (it was left, or
// kith-mcp wrote it), not per keystroke, which keeps the op log small. With nothing to
// wake it, followDrafts looks again every draftsRecheck.
const (
	draftsIdle    = 5 * time.Second
	draftsRecheck = time.Minute
)

// draftKeeper is a backend whose cache keeps drafts (local.Service): every draft
// stored, and a draft moved on another bridge taken in.
type draftKeeper interface {
	Drafts(ctx context.Context) ([]domain.StoredDraft, error)
	TakeDraft(ctx context.Context, from, to domain.StoredDraft) (domain.StoredDraft, bool, error)
}

// draftPlace is a room's draft, or one of its threads'.
type draftPlace struct {
	room domain.RoomID
	root domain.EventID
}

// draftPace is when each place's draft was last saved here, and what wakes
// followDrafts. Its zero value does nothing.
type draftPace struct {
	mu    sync.Mutex
	saved map[draftPlace]time.Time
	// tooLarge is each place found too large for an op, at its count of saves then.
	tooLarge map[draftPlace]uint64
	wake     chan struct{}
}

func newDraftPace() *draftPace {
	return &draftPace{saved: map[draftPlace]time.Time{}, tooLarge: map[draftPlace]uint64{}, wake: make(chan struct{}, 1)}
}

// poke wakes followDrafts.
func (p *draftPace) poke() {
	if p == nil {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// oweDraft records, before a save of d's place, that the mesh is owed it.
func (s *server) oweDraft(ctx context.Context, d domain.StoredDraft) {
	store, _, _, _, err := s.Paired.meshParts()
	if err != nil {
		return
	}
	if err := store.OweDraft(context.WithoutCancel(ctx), d.RoomID, d.ThreadRoot); err != nil {
		s.Log.Warn("note a draft owed to the mesh", "err", err)
	}
}

// savedDraft records a save of d's place made here, and wakes followDrafts.
func (s *server) savedDraft(d domain.StoredDraft) {
	if store, _, _, _, err := s.Paired.meshParts(); err == nil {
		store.SavedDraft(d.RoomID, d.ThreadRoot)
	}
	if s.drafts == nil {
		return
	}
	s.drafts.mu.Lock()
	s.drafts.saved[draftPlace{d.RoomID, d.ThreadRoot}] = time.Now()
	s.drafts.mu.Unlock()
	s.drafts.poke()
}

// followDrafts keeps drafts crossing the mesh: the cache takes each draft moved on
// another bridge (or lost here, as by a rebuild), and each place whose draft was saved
// here is written into the mesh at draftsIdle's pace. At start, after every change to
// the mesh, a save, a window leaving a room, and every draftsRecheck.
func (s *server) followDrafts(ctx context.Context) {
	keeper, ok := s.Backend.(draftKeeper)
	if !ok || s.drafts == nil {
		return
	}
	for {
		wait := draftsRecheck
		var meshChanged <-chan struct{}
		if store, _, _, signer, err := s.Paired.meshParts(); err == nil {
			meshChanged = store.Changed() // before reading, so nothing committed after is missed
			wait = s.syncDrafts(ctx, keeper, store, signer)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-meshChanged:
		case <-s.drafts.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// syncDrafts takes into the cache what moved in the mesh, writes into the mesh each
// owed place that has rested, and is how long until the next one has.
func (s *server) syncDrafts(ctx context.Context, keeper draftKeeper, store RosterStore, signer crypto.Signer) time.Duration {
	places := store.Drafts() // before the cache: a save made after is counted, so stays owed
	stored, err := keeper.Drafts(ctx)
	if err != nil {
		s.Log.Warn("read the drafts to pass to the mesh", "err", err)
		return draftsRecheck
	}
	cached := map[draftPlace]domain.StoredDraft{}
	for i := range stored {
		cached[draftPlace{stored[i].RoomID, stored[i].ThreadRoot}] = stored[i]
	}
	wait, changed := draftsRecheck, false
	for i := range places {
		d := &places[i]
		at := draftPlace{d.Room, d.Root}
		current, ok := cached[at]
		delete(cached, at)
		if !ok {
			current = domain.StoredDraft{RoomID: d.Room, ThreadRoot: d.Root}
		}
		current, took, ok := s.takeDraft(ctx, keeper, store, d, current)
		if !ok {
			continue
		}
		changed = changed || took
		if d.Owed {
			wait = min(wait, s.passDraft(ctx, store, signer, d, current))
		}
	}
	if s.oweUntaken(ctx, store, cached) {
		wait = 0
	}
	if changed {
		s.Notifications.rails.restate() // a draft is a room's state
		s.Streams.DraftsChanged()       // every window reads it again
	}
	return wait
}

// takeDraft has the cache, holding current, take d's draft when it moved in the mesh,
// or when the cache lost what it took (a rebuild) owing nothing: the cache's draft
// then, and whether it changed; ok is false, and the failure logged, when it could not.
// d.Owed is set when the cache keeps words of its own besides.
func (s *server) takeDraft(ctx context.Context, keeper draftKeeper, store RosterStore, d *domain.MeshDraft, current domain.StoredDraft) (_ domain.StoredDraft, changed, ok bool) {
	lost := !d.Owed && current.Empty() && !d.Draft.Empty()
	if !d.Moved && !lost {
		return current, false, true
	}
	from := d.Taken
	if !d.Moved {
		from = domain.StoredDraft{RoomID: d.Room, ThreadRoot: d.Root}
	}
	took, held, err := keeper.TakeDraft(ctx, from, d.Draft)
	switch {
	case err != nil:
	case held:
		d.Owed = d.Owed || !domain.SameDraft(took, d.Draft)
		took.RoomID, took.ThreadRoot = d.Room, d.Root
		changed, current = !domain.SameDraft(took, current), took
		err = store.TookDraft(ctx, *d, d.Owed)
	case d.Moved: // a room not held here keeps what it took
		err = store.TookDraft(ctx, *d, d.Owed)
	}
	if err != nil {
		s.Log.Warn("take a draft written on another bridge", "err", err)
		return current, changed, false
	}
	return current, changed, true
}

// passDraft writes current, the cache's draft of d's owed place, into the mesh once its
// saves have rested, and is how long until it should be looked at again.
func (s *server) passDraft(ctx context.Context, store RosterStore, signer crypto.Signer, d *domain.MeshDraft, current domain.StoredDraft) time.Duration {
	at := draftPlace{d.Room, d.Root}
	if rest := s.drafts.rest(at, d.Saves, s.windows); rest > 0 {
		return rest
	}
	wrote, err := store.WriteDraft(ctx, signer, *d, current)
	switch {
	case errors.Is(err, domain.ErrDraftTooLarge):
		s.drafts.tooLargeAt(at, d.Saves)
		s.Log.Warn("a draft stays on this bridge", "room", string(d.Room), "err", err)
	case err != nil:
		s.Log.Warn("write a draft into the mesh", "err", err)
	case !wrote:
		return 0 // the mesh holds versions not taken yet: take them, then write
	}
	return draftsRecheck
}

// oweUntaken records each place of untaken, the cache's drafts the mesh never took (a
// cache's from before drafts crossed), as owed to the mesh, reporting whether it
// recorded one. One of a room the mesh holds as merged moves with the merge instead.
func (s *server) oweUntaken(ctx context.Context, store RosterStore, untaken map[draftPlace]domain.StoredDraft) bool {
	merged := store.Aliases()
	owed := false
	for at := range untaken {
		if _, gone := merged[at.room]; gone {
			continue
		}
		if err := store.OweDraft(ctx, at.room, at.root); err != nil {
			s.Log.Warn("note a draft owed to the mesh", "err", err)
			continue
		}
		owed = true
	}
	return owed
}

// rest is how long until saves of at have rested draftsIdle: none when no window has
// its room open, or none was saved since kithd started; until the next save when it was
// found too large at saves.
func (p *draftPace) rest(at draftPlace, saves uint64, open *windows) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n, ok := p.tooLarge[at]; ok && n == saves {
		return draftsRecheck
	}
	saved, ok := p.saved[at]
	if !ok || open == nil || !open.hasOpen(at.room) {
		return 0
	}
	return time.Until(saved.Add(draftsIdle))
}

// tooLargeAt records at as too large for an op at its count of saves.
func (p *draftPace) tooLargeAt(at draftPlace, saves uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tooLarge[at] = saves
}
