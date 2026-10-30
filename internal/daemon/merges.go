package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// mergesRecheck is how often followMerges looks with nothing to wake it.
const mergesRecheck = time.Minute

// followMerges keeps what names a chat since merged into another (domain.RoomMerge)
// naming the chat it joined: the config's entries, written as a settings write is, and
// the scheduled messages. The merges are read from the rooms, each listing those merged
// into it, at start, after every change to the rooms and every mergesRecheck: a merge
// whose follow-up a restart or a hand edit cut off is followed up at the next look.
func (s *server) followMerges(ctx context.Context) {
	id, changes := s.Streams.changes.watchWith(subBuffer)
	defer s.Streams.changes.release(id)
	for {
		s.moveMerged(ctx)
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
			case <-timer.C:
				waiting = false
			}
		}
		timer.Stop()
	}
}

// moveMerged moves what names a merged chat to the chat it joined.
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
	if len(moved) == 0 {
		return
	}
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

// startMerges runs followMerges until ctx ends; wait returns once it has.
func (s *server) startMerges(ctx context.Context) (wait func()) {
	var running sync.WaitGroup
	running.Go(func() { s.followMerges(ctx) })
	return running.Wait
}
