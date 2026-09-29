package matrix

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// unreadBook is each room's last-known unread state, so mergeUnread can apply a
// partial sync delta (counts or receipt, not necessarily both), and when each room's
// read event happened (absent: unknown), so the read position only moves forward.
// One lock covers both, and a change is persisted under it (see update), so
// concurrent writers persist in the order they changed it.
type unreadBook struct {
	mu     sync.Mutex
	rooms  map[domain.RoomID]domain.Unread
	readTS map[domain.RoomID]int64
	// gens counts each room's writes, so a count taken outside the lock is written
	// back only onto the state it was taken from (see updateAt).
	gens map[domain.RoomID]uint64
}

// generation is how many times a room's state has been written.
func (u *unreadBook) generation(roomID domain.RoomID) uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.gens[roomID]
}

// updateAt is update, but only while the room is still at generation gen; current
// is false (and nothing is written) when another writer came between.
func (u *unreadBook) updateAt(roomID domain.RoomID, gen uint64,
	change func(cur *domain.Unread, readTS *int64) bool,
) (cur domain.Unread, changed, current bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gens[roomID] != gen {
		return u.rooms[roomID], false, false
	}
	cur, changed = u.updateLocked(roomID, change)
	return cur, changed, true
}

// get is a room's last-known unread state.
func (u *unreadBook) get(roomID domain.RoomID) domain.Unread {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.rooms[roomID]
}

// update runs change on a room's state and read time under the lock, and keeps what
// it leaves when it reports a change (or the room was known). change may persist the
// state it reports changed: it runs under the lock for exactly that.
func (u *unreadBook) update(roomID domain.RoomID, change func(cur *domain.Unread, readTS *int64) bool) (domain.Unread, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.updateLocked(roomID, change)
}

// updateLocked is update's body, under the caller's lock.
func (u *unreadBook) updateLocked(roomID domain.RoomID, change func(cur *domain.Unread, readTS *int64) bool) (domain.Unread, bool) {
	cur, known := u.rooms[roomID]
	ts := u.readTS[roomID]
	changed := change(&cur, &ts)
	if !changed && !known {
		return cur, false
	}
	if u.rooms == nil {
		u.rooms = make(map[domain.RoomID]domain.Unread)
		u.readTS = make(map[domain.RoomID]int64)
	}
	u.rooms[roomID] = cur
	if ts != 0 {
		u.readTS[roomID] = ts
	}
	if u.gens == nil {
		u.gens = make(map[domain.RoomID]uint64)
	}
	u.gens[roomID]++
	return cur, changed
}

// seed primes the book from the cache: each room's state, and the read times of the
// positions already placed.
func (u *unreadBook) seed(cached []domain.Unread, placed map[domain.RoomID]int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rooms == nil {
		u.rooms = make(map[domain.RoomID]domain.Unread, len(cached))
		u.readTS = make(map[domain.RoomID]int64, len(cached))
	}
	for _, c := range cached {
		u.rooms[c.RoomID] = c
		if ts, ok := placed[c.RoomID]; ok {
			u.readTS[c.RoomID] = ts
		}
	}
}
