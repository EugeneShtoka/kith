package daemon

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// readsRecheck is how often followReads looks with nothing to wake it.
const readsRecheck = time.Minute

// readKeeper is a backend whose cache keeps read positions (local.Service): how far a
// room or thread is read here, and the reads made on other bridges, kept with no
// receipt sent.
type readKeeper interface {
	HeldReads(ctx context.Context, of []domain.ReadPosition) ([]domain.ReadPosition, error)
	KeepReads(ctx context.Context, reads []domain.ReadPosition, marks map[domain.RoomID]bool) error
}

// readInMesh writes into the mesh how far each room or thread of of (Room and Root) is
// read here, once a read made in kith was kept by its network, which sends the receipt
// or owes it: every other bridge keeps the read and sends none (planning/design-mesh.md,
// decision 13). A read a network reports never passes here, so it stays out of the mesh
// (decision 14). A failure is logged; the read stands here.
func (s *server) readInMesh(ctx context.Context, of []domain.ReadPosition) {
	keeper, ok := s.Backend.(readKeeper)
	if !ok {
		return
	}
	store, _, _, signer, err := s.Paired.meshParts()
	if err != nil {
		return
	}
	ctx = context.WithoutCancel(ctx) // read here already: the mesh hears of it too
	held, err := keeper.HeldReads(ctx, of)
	if err == nil {
		err = store.Read(ctx, signer, held)
	}
	if err != nil {
		s.Log.Warn("write a read into the mesh", "err", err)
	}
}

// markInMesh writes room's unread mark, set or cleared here, into the mesh.
func (s *server) markInMesh(ctx context.Context, room domain.RoomID, marked bool) {
	store, _, _, signer, err := s.Paired.meshParts()
	if err != nil {
		return
	}
	if err := store.MarkUnread(context.WithoutCancel(ctx), signer, room, marked); err != nil {
		s.Log.Warn("write an unread mark into the mesh", "err", err)
	}
}

// followReads keeps the cache's read positions and marks as the mesh holds them: at
// start, after every change to the mesh or the rooms (a room of an account logged in
// later takes its position then) and every readsRecheck. First the reads the mesh holds
// of a room merged into another move under the room it joined, written as this
// bridge's own op: one written by a bridge that had not seen the merge yet is written
// again so.
func (s *server) followReads(ctx context.Context) {
	id, changes := s.Streams.changes.watchWith(subBuffer)
	defer s.Streams.changes.release(id)
	for {
		s.Paired.publishAliases(ctx, nil)
		var meshChanged <-chan struct{}
		if store, _, _, _, err := s.Paired.meshParts(); err == nil {
			meshChanged = store.Changed() // before reading, so nothing committed after is missed
		}
		s.keepReads(ctx)
		timer := time.NewTimer(readsRecheck)
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case c, ok := <-changes:
				if !ok {
					changes = nil
				}
				waiting = ok && c.what != domain.ChangeRooms
			case <-meshChanged:
				waiting = false
			case <-timer.C:
				waiting = false
			}
		}
		timer.Stop()
	}
}

// keepReads keeps in the cache what the mesh holds of reads: every position, and each
// mark written since the last look. A room the cache does not hold (one of an account
// this bridge does not run) is left alone; the mesh keeps its position.
func (s *server) keepReads(ctx context.Context) {
	keeper, ok := s.Backend.(readKeeper)
	if !ok {
		return
	}
	store, _, _, _, err := s.Paired.meshParts()
	if err != nil {
		return
	}
	if err := keeper.KeepReads(ctx, store.Reads(), store.TakeMarks()); err != nil {
		s.Log.Warn("keep the reads made on other bridges", "err", err)
	}
}
