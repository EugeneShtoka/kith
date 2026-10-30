package setup

import (
	"reflect"
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// MoveRooms makes every entry naming a room of moved by its ID name the room it moved
// to (a chat merged into another, domain.RoomMerge): a list's bare ID or room:<ID>, with
// its `not` or without, a tag's picked, a room's name. An entry naming a room by its
// name follows the room by itself.
func MoveRooms(cfg config.Config, moved map[domain.RoomID]domain.RoomID) config.Config {
	out := cfg.Clone()
	rewriteStrings(reflect.ValueOf(&out).Elem(), func(s string) string { return movedEntry(s, moved) })
	return out
}

// movedEntry is entry naming the room it names moved to, if moved holds it.
func movedEntry(entry string, moved map[domain.RoomID]domain.RoomID) string {
	body, not := notTerm(entry)
	prefix := ""
	if len(body) > len(config.NameTargetRoom) && strings.EqualFold(body[:len(config.NameTargetRoom)], config.NameTargetRoom) {
		prefix, body = body[:len(config.NameTargetRoom)], body[len(config.NameTargetRoom):]
	}
	into, ok := moved[domain.RoomID(body)]
	if !ok {
		return entry
	}
	if not {
		prefix = "not " + prefix
	}
	return prefix + string(into)
}
