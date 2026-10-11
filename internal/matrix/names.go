package matrix

import (
	"maps"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberNameMemo is each room's user-ID→display-name map; a present map means fully
// resolved. Maps are replaced, never mutated: callers hold them outside the lock.
// changes counts membership changes so a fetch overtaken by one installs nothing.
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

// putAt memoizes names fetched at change count seen, unless a change came since.
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

// rename applies a membership change ("" removes) to a resolved room and always counts
// it, so an in-flight fetch does not install a list without it.
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
