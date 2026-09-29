package matrix

import (
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Detecting sync changes to the room list, names or spaces. Rooms() and Spaces() read
// a cache only RefreshRooms/RefreshSpaces write, so a daemon with no client attached
// would otherwise never learn of them. The sync loop reports; the owner refreshes.

// roomStateTypes are the state events that change what Rooms() or Spaces() answer.
// Others' m.room.member is excluded: frequent, and it only matters for hero names.
var roomStateTypes = map[event.Type]bool{
	event.StateRoomName:       true,
	event.StateCanonicalAlias: true,
	event.StateSpaceChild:     true,
	event.StateSpaceParent:    true,
	event.StateCreate:         true,
	event.StateTopic:          true,
}

// roomsChanged reports whether a sync changes the room list, names or spaces. Our own
// membership changing adds or removes rooms.
func roomsChanged(resp *mautrix.RespSync, me id.UserID) bool {
	if resp == nil {
		return false
	}
	// Left rooms carry no state event to inspect.
	if len(resp.Rooms.Leave) > 0 {
		return true
	}
	if directChatsChanged(resp.AccountData.Events) {
		return true
	}
	for _, jr := range resp.Rooms.Join {
		if jr == nil {
			continue
		}
		if stateChanged(jr.State.Events, me) || stateChanged(jr.Timeline.Events, me) {
			return true
		}
	}
	return false
}

// stateChanged reports whether events rename, re-parent, or change our membership.
func stateChanged(events []*event.Event, me id.UserID) bool {
	for _, evt := range events {
		if evt == nil || evt.StateKey == nil {
			continue // not a state event
		}
		if roomStateTypes[evt.Type] {
			return true
		}
		if evt.Type == event.StateMember && *evt.StateKey == string(me) {
			return true
		}
	}
	return false
}

// directChatsChanged reports whether m.direct arrived (it decides Room.IsDirect).
func directChatsChanged(events []*event.Event) bool {
	for _, evt := range events {
		if evt != nil && evt.Type == event.AccountDataDirectChats {
			return true
		}
	}
	return false
}
