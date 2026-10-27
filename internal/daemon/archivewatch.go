package daemon

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// archiveWatch mirrors membership in `push_archive = true` tags into each room's
// network archive, per profile: a profile pushes the rooms it holds that are in its
// pushing tags as it has them (its own archive, or the shared one). Pushing profiles
// add up: a room enters the network archive when a pushing profile starts holding it,
// and leaves when one stops while no pushing profile holds it, so two profiles never
// undo each other.
//
// A profile holds a room for kith's own reasons (its rule, hand filing, a filter), not
// because the network reports it archived: that report ([<network>.archive] follow)
// echoes kith's own push and would otherwise keep a room archived for good. Only an
// archive kith made is undone; a chat the network already had archived (the phone
// archived it) is left as it is. The network is told differences only, and a refused
// call is tried again on the next reconcile. All of it is in memory and starts empty,
// so a daemon restart re-pushes nothing, and undoes no archive made before it.
type archiveWatch struct {
	log *slog.Logger

	mu sync.Mutex
	// shared is the tags as the config has them; profiles push with their own where
	// they have them.
	shared   domain.TagSet
	profiles []domain.Profile
	// pushed is, per pushing profile and pushing tag (both lower-cased), the rooms the
	// profile held in the tag as of the last reconcile.
	pushed map[string]map[string]map[domain.RoomID]bool
	// ours is, per pushing tag, the rooms kith archived on their network and has not
	// unarchived since: the only ones a profile letting go unarchives.
	ours map[string]map[domain.RoomID]bool
	// running is a reconcile under way; dirty asks it for one more with fresh facts.
	running, dirty bool
}

func newArchiveWatch() *archiveWatch {
	return &archiveWatch{
		pushed: map[string]map[string]map[domain.RoomID]bool{},
		ours:   map[string]map[domain.RoomID]bool{},
	}
}

// setLog gives the watcher somewhere to say which rooms it pushed; nil is silent.
func (w *archiveWatch) setLog(log *slog.Logger) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = log
}

// setPushing replaces the tags and the profiles. A profile, or a tag of one, that no
// longer pushes does not have its rooms cleared on the network: they were archived
// deliberately, and only the person may undo them. Its snapshot is forgotten, so
// pushing again starts fresh.
func (w *archiveWatch) setPushing(shared domain.TagSet, profiles []domain.Profile) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shared, w.profiles = shared, profiles
	w.prune()
}

// prune forgets the snapshot of every profile and tag that no longer pushes, and
// what kith archived for a tag no profile pushes. Call with mu held.
func (w *archiveWatch) prune() {
	pushing := pushingTags(w.shared, w.profiles)
	anyPushes := map[string]bool{}
	for _, tags := range pushing {
		maps.Copy(anyPushes, tags)
	}
	for profile, tags := range w.pushed {
		for tag := range tags {
			if !pushing[profile][tag] {
				delete(tags, tag)
			}
		}
		if len(tags) == 0 {
			delete(w.pushed, profile)
		}
	}
	for tag := range w.ours {
		if !anyPushes[tag] {
			delete(w.ours, tag)
		}
	}
}

// pushingTags is, per profile (lower-cased), its pushing tags (lower-cased) as it has
// them; a profile pushing none is left out.
func pushingTags(shared domain.TagSet, profiles []domain.Profile) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range profiles {
		names := tagsOf(shared, p).PushArchiveNames()
		if len(names) == 0 {
			continue
		}
		keys := make(map[string]bool, len(names))
		for _, name := range names {
			keys[tagKey(name)] = true
		}
		out[profileSlot(p.Name)] = keys
	}
	return out
}

// tagsOf is the tags as profile p has them.
func tagsOf(shared domain.TagSet, p domain.Profile) domain.TagSet {
	if p.Tags != nil {
		return *p.Tags
	}
	return shared
}

// profileSlot is a profile's name as the snapshot holds it.
func profileSlot(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// setArchiver is the function that actually calls the network: Router.SetRoomArchived.
type setArchiver func(ctx context.Context, roomID domain.RoomID, archived bool) error

// kick runs a reconcile with facts read now, or asks the one running for another once
// it ends: reconciles never overlap, and the last reads the newest facts, so a push
// working from older facts cannot land after one from newer ones. It returns at once.
func (w *archiveWatch) kick(read func(context.Context) ([]domain.RoomFacts, error), setArchive setArchiver) {
	w.mu.Lock()
	w.dirty = true
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()
	// A reconcile can block on an adapter's network call; the caller must not wait.
	go func() {
		ctx := context.Background()
		for {
			w.mu.Lock()
			if !w.dirty {
				w.running = false
				w.mu.Unlock()
				return
			}
			w.dirty = false
			log := w.log
			w.mu.Unlock()
			facts, err := read(ctx)
			if err != nil {
				if log != nil {
					log.Warn("push_archive: skipping reconcile; scope read failed", "err", err)
				}
				continue
			}
			w.reconcile(ctx, facts, setArchive)
		}
	}()
}

// pushState is one reconcile's view: the snapshot it started from, what the profiles
// hold now, and what kith has archived, updated as calls succeed.
type pushState struct {
	pushing    map[string]map[string]bool
	previously map[string]map[string]map[domain.RoomID]bool
	// seen is what each pushing profile holds now, per tag; held their union.
	seen map[string]map[string]map[domain.RoomID]bool
	held map[string]map[domain.RoomID]bool
	ours map[string]map[domain.RoomID]bool
	byID map[domain.RoomID]*domain.RoomFacts
	log  *slog.Logger
}

// reconcile tells setArchive what changed since the last one: rooms a pushing
// profile newly holds in a pushing tag are archived, unless the network has them
// archived already or another pushing profile pushed them; rooms kith archived that a
// pushing profile no longer holds are unarchived once no pushing profile holds them.
// Called by kick, one at a time.
func (w *archiveWatch) reconcile(ctx context.Context, facts []domain.RoomFacts, setArchive setArchiver) {
	if setArchive == nil {
		return
	}
	w.mu.Lock()
	shared, profiles := w.shared, w.profiles
	s := pushState{pushing: pushingTags(shared, profiles), log: w.log, ours: cloneRooms(w.ours)}
	s.previously = make(map[string]map[string]map[domain.RoomID]bool, len(w.pushed))
	for profile, tags := range w.pushed {
		s.previously[profile] = cloneRooms(tags)
	}
	w.mu.Unlock()

	s.seen, s.held, s.byID = holdings(facts, shared, profiles, s.pushing)
	s.archiveNew(ctx, setArchive)
	s.unarchiveGone(ctx, setArchive)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pushed, w.ours = s.seen, s.ours
	w.prune() // what stopped pushing while this ran
}

// holdings is what each pushing profile holds in each of its pushing tags (seen), and
// their union (held), with each room's facts by ID. A profile holds a room for kith's
// own reasons: the network reporting it archived is left out.
func holdings(facts []domain.RoomFacts, shared domain.TagSet, profiles []domain.Profile, pushing map[string]map[string]bool) (
	seen map[string]map[string]map[domain.RoomID]bool, held map[string]map[domain.RoomID]bool, byID map[domain.RoomID]*domain.RoomFacts,
) {
	seen = map[string]map[string]map[domain.RoomID]bool{}
	held = map[string]map[domain.RoomID]bool{}
	byID = make(map[domain.RoomID]*domain.RoomFacts, len(facts))
	for i := range facts {
		byID[domain.RoomID(facts[i].ID)] = &facts[i]
	}
	for _, p := range profiles {
		tags := pushing[profileSlot(p.Name)]
		if len(tags) == 0 {
			continue
		}
		mine := make(map[string]map[domain.RoomID]bool, len(tags))
		for tag := range tags {
			mine[tag] = map[domain.RoomID]bool{}
		}
		seen[profileSlot(p.Name)] = mine
		judge := tagsOf(shared, p)
		for i := range facts {
			if !p.Holds(facts[i]) {
				continue // a profile pushes only the rooms it holds
			}
			own := facts[i]
			own.ArchivedIn = ""
			for _, name := range judge.Of(own) {
				key := tagKey(name)
				if !tags[key] {
					continue
				}
				roomID := domain.RoomID(own.ID)
				mine[key][roomID] = true
				if held[key] == nil {
					held[key] = map[domain.RoomID]bool{}
				}
				held[key][roomID] = true
			}
		}
	}
	return seen, held, byID
}

// archiveNew archives the rooms a pushing profile newly holds. One the network
// already has archived is not kith's; one it refuses is left out of the snapshot, so
// the next reconcile asks again.
func (s *pushState) archiveNew(ctx context.Context, setArchive setArchiver) {
	for tag, rooms := range s.held {
		for roomID := range rooms {
			if pushedBefore(s.previously, tag, roomID) || archiveMatches(*s.byID[roomID], tag) {
				continue
			}
			if err := setArchive(ctx, roomID, true); err != nil {
				warnRefused(s.log, "archive", roomID, tag, err)
				for _, tags := range s.seen {
					delete(tags[tag], roomID)
				}
				continue
			}
			mark(s.ours, tag, roomID)
			if s.log != nil {
				s.log.Info("push_archive: archived", "room", roomID, "tag", tag)
			}
		}
	}
}

// unarchiveGone unarchives the rooms kith archived that a still-pushing profile held
// and no longer does, once no pushing profile holds them. A refused call stays in the
// profile's snapshot, so the next reconcile asks again.
func (s *pushState) unarchiveGone(ctx context.Context, setArchive setArchiver) {
	asked := map[string]map[domain.RoomID]bool{}
	for profile, tags := range s.previously {
		for tag, rooms := range tags {
			if !s.pushing[profile][tag] {
				continue // stopped pushing: its rooms stay as they are
			}
			for roomID := range rooms {
				if s.seen[profile][tag][roomID] || s.held[tag][roomID] || !s.ours[tag][roomID] || asked[tag][roomID] {
					continue
				}
				mark(asked, tag, roomID)
				if err := setArchive(ctx, roomID, false); err != nil {
					warnRefused(s.log, "unarchive", roomID, tag, err)
					s.seen[profile][tag][roomID] = true
					continue
				}
				delete(s.ours[tag], roomID)
				if s.log != nil {
					s.log.Info("push_archive: unarchived", "room", roomID, "tag", tag)
				}
			}
		}
	}
}

// mark records room under tag.
func mark(set map[string]map[domain.RoomID]bool, tag string, room domain.RoomID) {
	if set[tag] == nil {
		set[tag] = map[domain.RoomID]bool{}
	}
	set[tag][room] = true
}

// cloneRooms copies a per-tag set of rooms.
func cloneRooms(in map[string]map[domain.RoomID]bool) map[string]map[domain.RoomID]bool {
	out := make(map[string]map[domain.RoomID]bool, len(in))
	for tag, rooms := range in {
		out[tag] = maps.Clone(rooms)
	}
	return out
}

// pushedBefore reports whether any pushing profile held room in tag last time.
func pushedBefore(previously map[string]map[string]map[domain.RoomID]bool, tag string, room domain.RoomID) bool {
	for _, tags := range previously {
		if tags[tag][room] {
			return true
		}
	}
	return false
}

// warnRefused logs a network refusing a push; a network without an archive, or one
// that is off, is not news.
func warnRefused(log *slog.Logger, what string, room domain.RoomID, tag string, err error) {
	if log == nil || errors.Is(err, api.ErrNotOnNetwork) || errors.Is(err, api.ErrNetworkOff) {
		return
	}
	log.Warn("push_archive: failed to "+what, "room", room, "tag", tag, "err", err)
}

// archiveMatches reports whether the room is already archived on the network,
// and the archive the network uses is the tag that pushes.
func archiveMatches(f domain.RoomFacts, tagKey string) bool {
	return f.ArchivedIn != "" && tagKey == strings.ToLower(f.ArchivedIn)
}
