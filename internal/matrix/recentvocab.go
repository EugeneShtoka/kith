package matrix

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/vocab"
)

// Word completion from recent history (internal/vocab): each window is read from the
// cache the first time it is needed, then kept up to date as messages arrive. An edit or
// a redaction cannot be counted out, so it drops the windows it touches; so does a
// window's growing well past its size. Either way the next completion rebuilds it.

// How many messages each window counts: this conversation's, its space's, your own, and
// everyone's.
const (
	roomWindow   = 1000
	spaceWindow  = 3000
	mineWindow   = 2000
	globalWindow = 5000
	// scopedWindows is how many room and space windows are kept, each.
	scopedWindows = 32
)

// recentVocab holds the windows. mu guards all of it: completions read from RPC
// goroutines and messages arrive on the sync goroutine. A build reads the cache under
// mu, so a message arriving meanwhile waits rather than being counted into nothing.
type recentVocab struct {
	mu     sync.Mutex
	rooms  scopedWindowSet // keyed by the conversation's rooms (its upgrade chain)
	spaces scopedWindowSet // keyed by the space's rooms
	mine   *vocab.Window
	global *vocab.Window
}

// scopedWindow is a window over a set of rooms.
type scopedWindow struct {
	key    string
	rooms  []domain.RoomID // sorted
	window *vocab.Window
}

// scopedWindowSet is a small LRU: most recently used last.
type scopedWindowSet []scopedWindow

func roomsKey(rooms []domain.RoomID) (string, []domain.RoomID) {
	sorted := slices.Clone(rooms)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	parts := make([]string, len(sorted))
	for i, r := range sorted {
		parts[i] = string(r)
	}
	return strings.Join(parts, " "), sorted
}

// get is the fresh window for rooms, building it (and evicting the least recently used)
// when there is none.
func (s *scopedWindowSet) get(rooms []domain.RoomID, build func([]domain.RoomID) *vocab.Window) *vocab.Window {
	key, sorted := roomsKey(rooms)
	for i, w := range *s {
		if w.key != key {
			continue
		}
		*s = append(slices.Delete(*s, i, i+1), w)
		if w.window.Stale() {
			break
		}
		return w.window
	}
	*s = slices.DeleteFunc(*s, func(w scopedWindow) bool { return w.key == key })
	window := build(sorted)
	if len(*s) >= scopedWindows {
		*s = slices.Delete(*s, 0, 1)
	}
	*s = append(*s, scopedWindow{key: key, rooms: sorted, window: window})
	return window
}

// each calls fn with every window covering room.
func (s scopedWindowSet) each(room domain.RoomID, fn func(*vocab.Window)) {
	for _, w := range s {
		if _, ok := slices.BinarySearch(w.rooms, room); ok {
			fn(w.window)
		}
	}
}

// drop forgets every window covering room.
func (s *scopedWindowSet) drop(room domain.RoomID) {
	*s = slices.DeleteFunc(*s, func(w scopedWindow) bool {
		_, ok := slices.BinarySearch(w.rooms, room)
		return ok
	})
}

// rank scores req.Prefix over the windows its scope names, building any not yet held.
// Scope rules: the room term only for a room scope with rooms, the space term unless
// global, the mine term with an account.
func (v *recentVocab) rank(ctx context.Context, cache *db.Cache, req domain.CompleteRequest, me string, limit int) ([]domain.WordCandidate, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var firstErr error
	read := func(rooms domain.RoomSet, sender string, n int) *vocab.Window {
		w := vocab.NewWindow(n)
		bodies, err := cache.RecentBodies(ctx, rooms, sender, n)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		for _, body := range bodies {
			w.Add(body)
		}
		return w
	}

	var scope vocab.Scope
	if req.Scope != scopeSpace && req.Scope != scopeGlobal && len(req.RoomIDs) > 0 {
		scope.Room = v.rooms.get(req.RoomIDs, func(r []domain.RoomID) *vocab.Window { return read(domain.TheseRooms(r), "", roomWindow) })
	}
	if req.Scope != scopeGlobal && len(req.SpaceRooms) > 0 {
		scope.Space = v.spaces.get(req.SpaceRooms, func(r []domain.RoomID) *vocab.Window { return read(domain.TheseRooms(r), "", spaceWindow) })
	}
	if me != "" {
		if v.mine == nil || v.mine.Stale() {
			v.mine = read(domain.EveryRoom(), me, mineWindow)
		}
		scope.Mine = v.mine
	}
	if v.global == nil || v.global.Stale() {
		v.global = read(domain.EveryRoom(), "", globalWindow)
	}
	scope.Global = v.global
	if firstErr != nil {
		// A window that could not be read is not kept, so the next completion retries.
		v.forgetAll()
		return nil, firstErr
	}
	return vocab.Rank(req.Prefix, scope, limit), nil
}

// The [complete] scope names (internal/matrix reads no config).
const (
	scopeSpace  = "space"
	scopeGlobal = "global"
)

// added counts a new message into every window holding it.
func (v *recentVocab) added(msg domain.Message, me string) {
	if msg.Body == "" || msg.Redacted {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if msg.RevisionID != "" && msg.RevisionID != msg.ID {
		// An edit replaces words that cannot be counted out.
		v.changedLocked(msg.RoomID)
		return
	}
	count := func(w *vocab.Window) { w.Add(msg.Body) }
	v.rooms.each(msg.RoomID, count)
	v.spaces.each(msg.RoomID, count)
	if v.mine != nil && me != "" && msg.Sender == me {
		v.mine.Add(msg.Body)
	}
	if v.global != nil {
		v.global.Add(msg.Body)
	}
}

// changed drops what a redaction in room makes wrong.
func (v *recentVocab) changed(room domain.RoomID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.changedLocked(room)
}

func (v *recentVocab) changedLocked(room domain.RoomID) {
	v.rooms.drop(room)
	v.spaces.drop(room)
	v.mine, v.global = nil, nil
}

// cleared drops every window, for an emptied cache.
func (v *recentVocab) cleared() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.forgetAll()
}

// forgetAll drops every window (a cleared cache, or a failed read). Caller holds mu.
func (v *recentVocab) forgetAll() {
	v.rooms, v.spaces, v.mine, v.global = nil, nil, nil, nil
}
