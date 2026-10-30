package crdt

import (
	"fmt"
	"strings"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// aliasPrefix starts the key of a room merged into another; the room's ID follows.
const aliasPrefix = "alias/"

// AliasKey is the key of room, merged into another: a WhatsApp chat kept by the
// person's LID until a bridge learned their number (planning/design-mesh.md, decision
// 17). Its value is the room it joined. Any bridge writes it as it merges, and every
// bridge running the room's account merges its own the same way.
func AliasKey(room string) string { return aliasPrefix + room }

// readAliasWrite checks w as an alias write, reporting whether it is one: a room's key,
// naming another room.
func readAliasWrite(w Write) (bool, error) {
	room, ok := strings.CutPrefix(w.Key, aliasPrefix)
	switch {
	case !ok:
		return false, nil
	case room == "" || len(w.Value) == 0 || string(w.Value) == room:
		return true, fmt.Errorf("%w: %s names no other room", ErrBadWrite, w.Key)
	}
	return true, nil
}

// readWrite checks a write by origin that is not the roster's: an alias, a read
// position or mark, or a bridge's own accounts. Any other key is of a kind not kept
// yet.
func readWrite(origin devicekey.ID, w Write) error {
	if alias, err := readAliasWrite(w); alias {
		return err
	}
	if read, err := readReadWrite(w); read {
		return err
	}
	return readAccountsWrite(origin, w)
}

// Aliases is every room merged into another, and the room it joined: of writes naming
// different rooms concurrently, the later by (clock, origin), as every replica reads it.
func (s State) Aliases() map[string]string {
	out := map[string]string{}
	for key, entries := range s.Entries {
		room, ok := strings.CutPrefix(key, aliasPrefix)
		if !ok || len(entries) == 0 {
			continue
		}
		best := entries[0]
		for _, e := range entries[1:] {
			if later(e, best) {
				best = e
			}
		}
		out[room] = string(best.Value)
	}
	return out
}
