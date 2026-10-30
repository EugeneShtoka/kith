package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// mergesRecheck is how often followMerges looks with nothing to wake it.
const mergesRecheck = time.Minute

// roomMerger is a backend that merges a room another bridge merged (route.Merger).
type roomMerger interface {
	MergeRoom(ctx context.Context, from, into domain.RoomID) error
}

// followMerges keeps what names a chat since merged into another (domain.RoomMerge)
// naming the chat it joined: the config's entries, written as a settings write is, and
// the scheduled messages. It writes each merge into the mesh (alias/<room>), and merges
// here each one another bridge wrote there, for an account this bridge runs, without
// waiting to learn the number itself. The merges are read from the rooms, each listing
// those merged into it, and from the mesh, at start, after every change to the rooms or
// the mesh and every mergesRecheck: a merge whose follow-up a restart or a hand edit
// cut off is followed up at the next look.
func (s *server) followMerges(ctx context.Context) {
	id, changes := s.Streams.changes.watchWith(subBuffer)
	defer s.Streams.changes.release(id)
	for {
		s.moveMerged(ctx)
		var meshChanged <-chan struct{}
		if store, _, _, _, err := s.Paired.meshParts(); err == nil {
			meshChanged = store.Changed()
		}
		timer := time.NewTimer(mergesRecheck)
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

// moveMerged merges what the mesh holds merged, writes this bridge's merges into the
// mesh, and moves what names a merged chat to the chat it joined.
func (s *server) moveMerged(ctx context.Context) {
	rooms, err := s.Backend.Rooms(ctx)
	if err != nil {
		return // the next look reads them
	}
	moved := map[domain.RoomID]domain.RoomID{}
	for i := range rooms {
		for _, from := range rooms[i].Merged {
			moved[from] = rooms[i].ID
		}
	}
	s.mergeFromMesh(ctx, moved)
	if len(moved) == 0 {
		return
	}
	s.Paired.publishAliases(ctx, moved)
	if err = s.Scheduler.MoveRooms(moved); err != nil {
		s.Log.Warn("send scheduled messages to the chats theirs merged into", "err", err)
	}
	snap, err := s.Config.Read()
	if err != nil {
		return // one that does not read is the reload's to report
	}
	next := setup.MoveRooms(snap.Config, moved)
	if config.Encode(next) == config.Encode(snap.Config) {
		return
	}
	// A hand edit landing meanwhile refuses this write; the next look writes over it.
	written, err := s.Config.Write(ctx, snap.Revision, next, s.Daemon.CheckConfig, s.Reload)
	if err != nil {
		s.Log.Warn("name merged chats in the config by the chats they joined", "err", err)
		return
	}
	s.Log.Info("the config names merged chats by the chats they joined")
	s.Streams.Configured(written)
}

// mergeFromMesh merges here each room the mesh holds as merged and this bridge does
// not (merged, here): the room's network leaves one of an account it does not run, and
// the mesh keeps it, inert. A merge done here shows in the rooms, which it changes.
func (s *server) mergeFromMesh(ctx context.Context, merged map[domain.RoomID]domain.RoomID) {
	merger, ok := s.Backend.(roomMerger)
	if !ok {
		return
	}
	for from, into := range s.Paired.meshAliases() {
		if merged[from] == into {
			continue
		}
		if err := merger.MergeRoom(ctx, from, into); err != nil && !errors.Is(err, api.ErrNotOnNetwork) {
			s.Log.Warn("merge a chat another bridge merged", "room", from, "err", err)
		}
	}
}

// publishAliases writes the rooms merged here into the mesh.
func (d *Devices) publishAliases(ctx context.Context, aliases map[domain.RoomID]domain.RoomID) {
	store, _, _, signer, err := d.meshParts()
	if err != nil {
		return
	}
	if err := store.PublishAliases(ctx, signer, aliases); err != nil {
		d.log.Debug("publish this bridge's merged chats to the mesh", "err", err)
	}
}

// meshAliases is every room the mesh holds as merged into another; none without a
// store.
func (d *Devices) meshAliases() map[domain.RoomID]domain.RoomID {
	store, _, _, _, err := d.meshParts()
	if err != nil {
		return nil
	}
	return store.Aliases()
}

// startMerges runs followMerges until ctx ends; wait returns once it has.
func (s *server) startMerges(ctx context.Context) (wait func()) {
	var running sync.WaitGroup
	running.Go(func() { s.followMerges(ctx) })
	return running.Wait
}
