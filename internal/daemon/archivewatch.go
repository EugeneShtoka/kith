package daemon

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// archiveWatch mirrors membership in `push_archive = true` tags into each room's
// network archive. The source of truth is the local tag; a room entering the tag
// calls SetArchived(true), a room leaving calls SetArchived(false). The snapshot
// is in-memory and seeded on each Reconcile from the current scope so a daemon
// restart does not re-push the whole set.
type archiveWatch struct {
	log *slog.Logger

	mu sync.Mutex
	// pushing is each push_archive tag's name, lower-cased for stable comparison.
	pushing map[string]bool
	// pushed is the (tag → room → true) membership we have already told the
	// adapter about. Rooms a Reconcile sees in a pushing tag that are not here
	// have SetArchived(true) called; rooms here but no longer in the tag have
	// SetArchived(false) called. First Reconcile after a daemon start seeds from
	// the facts: the room's own ArchivedIn is the server's current state, so a
	// restart does not re-push.
	pushed map[string]map[domain.RoomID]bool
}

func newArchiveWatch() *archiveWatch {
	return &archiveWatch{
		pushing: map[string]bool{},
		pushed:  map[string]map[domain.RoomID]bool{},
	}
}

// setLog gives the watcher somewhere to say which rooms it pushed; nil is silent.
func (w *archiveWatch) setLog(log *slog.Logger) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = log
}

// setPushing replaces the names of push_archive tags; a tag no longer pushing
// does not have its membership cleared on the network — the user tagged those
// rooms deliberately and only the user may undo them. The snapshot for the
// dropped tag is forgotten so a later re-enable starts fresh (will re-push).
func (w *archiveWatch) setPushing(tags domain.TagSet) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pushing = map[string]bool{}
	for _, name := range tags.PushArchiveNames() {
		key := tagKey(name)
		w.pushing[key] = true
	}
	for key := range w.pushed {
		if !w.pushing[key] {
			delete(w.pushed, key)
		}
	}
}

// setArchiver is the function that actually calls the network: Router.SetRoomArchived.
// Setting it nil disables pushing (useful for tests and a transitory kithd that is
// shutting down).
type setArchiver func(ctx context.Context, roomID domain.RoomID, archived bool) error

// reconcile walks the current scope and tells setArchive the diffs. A failed push
// is logged and not retried this pass; the next Reconcile sees the still-missing
// row and tries again.
func (w *archiveWatch) reconcile(ctx context.Context, facts []domain.RoomFacts, setArchive setArchiver) {
	pushing, previously, log, ok := w.snapshot()
	if !ok || setArchive == nil {
		return
	}
	seen := make(map[string]map[domain.RoomID]bool, len(pushing))
	for k := range pushing {
		seen[k] = map[domain.RoomID]bool{}
	}
	for i := range facts {
		w.pushArchiveNewMembers(ctx, &facts[i], pushing, previously, seen, setArchive, log)
	}
	for key, rooms := range previously {
		if !pushing[key] {
			continue
		}
		w.pushUnarchiveGone(ctx, key, rooms, seen[key], setArchive, log)
	}
	w.mu.Lock()
	w.pushed = seen
	w.mu.Unlock()
}

// snapshot copies the lock-held reconcile inputs so the diff loops can run
// without holding the mutex.
func (w *archiveWatch) snapshot() (pushing map[string]bool, previously map[string]map[domain.RoomID]bool, log *slog.Logger, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pushing) == 0 {
		return nil, nil, nil, false
	}
	pushing = make(map[string]bool, len(w.pushing))
	maps.Copy(pushing, w.pushing)
	previously = make(map[string]map[domain.RoomID]bool, len(w.pushed))
	for k, rooms := range w.pushed {
		previously[k] = maps.Clone(rooms)
	}
	return pushing, previously, w.log, true
}

// pushArchiveNewMembers tells setArchive about rooms that just joined a pushing
// tag, and records what the scope now sees so pushUnarchiveGone can diff.
func (w *archiveWatch) pushArchiveNewMembers(ctx context.Context, f *domain.RoomFacts, pushing map[string]bool, previously, seen map[string]map[domain.RoomID]bool, setArchive setArchiver, log *slog.Logger) {
	roomID := domain.RoomID(f.ID)
	for _, tag := range f.Tags {
		key := tagKey(tag)
		if !pushing[key] {
			continue
		}
		seen[key][roomID] = true
		if previously[key][roomID] || archiveMatches(*f, key) {
			continue
		}
		if err := setArchive(ctx, roomID, true); err != nil {
			if log != nil && !errors.Is(err, api.ErrNotOnNetwork) && !errors.Is(err, api.ErrNetworkOff) {
				log.Warn("push_archive: failed to archive", "room", f.ID, "tag", tag, "err", err)
			}
			continue
		}
		if log != nil {
			log.Info("push_archive: archived", "room", f.ID, "tag", tag)
		}
	}
}

// pushUnarchiveGone tells setArchive about rooms we have pushed before that are
// no longer in the pushing tag.
func (w *archiveWatch) pushUnarchiveGone(ctx context.Context, tagKey string, previously, seen map[domain.RoomID]bool, setArchive setArchiver, log *slog.Logger) {
	for roomID := range previously {
		if seen[roomID] {
			continue
		}
		if err := setArchive(ctx, roomID, false); err != nil {
			if log != nil && !errors.Is(err, api.ErrNotOnNetwork) && !errors.Is(err, api.ErrNetworkOff) {
				log.Warn("push_archive: failed to unarchive", "room", roomID, "tag", tagKey, "err", err)
			}
			continue
		}
		if log != nil {
			log.Info("push_archive: unarchived", "room", roomID, "tag", tagKey)
		}
	}
}

// archiveMatches reports whether the room is already archived on the network,
// and the archive the network uses is the tag that pushes.
func archiveMatches(f domain.RoomFacts, tagKey string) bool {
	if f.ArchivedIn == "" {
		return false
	}
	// ArchivedIn carries the tag name the network's archive is mapped to; the
	// usual case is "Archived".
	return tagKey == stringsLower(f.ArchivedIn)
}

// stringsLower avoids the strings import (one use in this file).
func stringsLower(s string) string {
	out := make([]byte, len(s))
	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
