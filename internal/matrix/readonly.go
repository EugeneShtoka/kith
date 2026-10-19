package matrix

import (
	"context"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// readOnlyRooms is which of rooms this account may not post a message in, by the power
// levels the state store holds. A room whose levels are not known yet counts as
// writable: the homeserver refuses a send it would not take anyway.
func (b *InProc) readOnlyRooms(ctx context.Context, rooms []id.RoomID) map[domain.RoomID]bool {
	out := make(map[domain.RoomID]bool, len(rooms))
	store := b.stateStore()
	if store == nil {
		return out
	}
	for _, roomID := range rooms {
		levels, err := store.GetPowerLevels(ctx, roomID)
		if err != nil || levels == nil {
			b.debugIf(ctx, err, "read power levels", "room", roomID)
			out[domain.RoomID(roomID)] = false
			continue
		}
		out[domain.RoomID(roomID)] = readOnly(levels, b.client.UserID)
	}
	return out
}

// readOnly reports whether me is below the level a message needs.
func readOnly(levels *event.PowerLevelsEventContent, me id.UserID) bool {
	return levels.GetUserLevel(me) < levels.GetEventLevel(event.EventMessage)
}
