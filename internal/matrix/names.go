package matrix

import (
	"maps"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberNameMemo is each room's user-ID→display-name map, one request per room. A
// present map means the room is fully resolved. Maps are replaced, never mutated,
// because callers hold them outside the lock. changes counts each room's membership
// changes, so a fetch that a change overtook installs nothing (putAt).
type memberNameMemo struct {
	mu      sync.Mutex
	rooms   map[domain.RoomID]map[string]string
	changes map[domain.RoomID]uint64
}

// seen is room's change count, taken before a fetch.
func (n *memberNameMemo) seen(roomID domain.RoomID) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.changes[roomID]
}

// putAt memoizes names fetched when the room's change count was seen, unless a change
// came since: the list predates it, and the next lookup fetches again.
func (n *memberNameMemo) putAt(roomID domain.RoomID, seen uint64, names map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.changes[roomID] != seen {
		return
	}
	if n.rooms == nil {
		n.rooms = make(map[domain.RoomID]map[string]string)
	}
	n.rooms[roomID] = names
}

// get is a room's memoized names, and whether the room has been resolved.
func (n *memberNameMemo) get(roomID domain.RoomID) (map[string]string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	names, ok := n.rooms[roomID]
	return names, ok
}

// rename applies a membership change ("" removes) to a room already resolved; a room
// not yet resolved is left alone, its first lookup fetches the whole list. Either way
// it is counted, so a fetch already under way does not install a list without it.
func (n *memberNameMemo) rename(roomID domain.RoomID, userID, name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.changes == nil {
		n.changes = make(map[domain.RoomID]uint64)
	}
	n.changes[roomID]++
	current, ok := n.rooms[roomID]
	if !ok {
		return
	}
	next := make(map[string]string, len(current)+1)
	maps.Copy(next, current)
	if name == "" {
		delete(next, userID)
	} else {
		next[userID] = name
	}
	n.rooms[roomID] = next
}
