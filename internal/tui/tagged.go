package tui

import (
	"reflect"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What the tags make of each room: which hold it, whether it is silenced, the tag it
// shows under alone, and which spaces it leaves. Every rail row asks this per room per
// draw, so it is judged with the rail and memoized with it (rail.go).

// identity is a map's identity; nil is 0.
func identity(m any) uintptr {
	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Map || v.IsNil() {
		return 0
	}
	return v.Pointer()
}

// tagsOf is what the tags make of room, as the rail judged it; a room the rail does not
// hold is judged on its own.
func (v unreadView) tagsOf(room domain.Room) domain.RoomTags {
	if judged, ok := v.currentRail().built.rail.Judged[room.ID]; ok {
		return judged
	}
	return v.computeTags(room)
}

// computeTags judges every tag on one room (domain.TagSet.Judge).
func (v unreadView) computeTags(room domain.Room) domain.RoomTags {
	return v.tags.Judge(v.factsOf(room), v.rawState(room))
}

// factsOf is what a place entry can match about room.
func (v unreadView) factsOf(room domain.Room) domain.RoomFacts {
	facts, ok := v.roomFacts[room.ID]
	if !ok && v.facts != nil {
		facts = v.facts(room) // arrived since the last refresh: the slow way, once
	}
	return facts
}

// tagState is the state tag i judges room in: the room's own for a silent tag, read
// for any other while a silent tag holds the room.
func (v unreadView) tagState(i int, room domain.Room) domain.RoomState {
	state := v.rawState(room)
	if !v.tags.At(i).Silent && v.silenced(room) {
		state.Unread, state.Mention = false, false
	}
	return state
}

// rawState is the room's state before any tag silences it.
func (v unreadView) rawState(room domain.Room) domain.RoomState {
	_, highlights := v.count(room)
	_, draft := v.drafts[room.ID]
	return domain.RoomState{
		Unread:  v.counts[room.ID].HasUnread(v.local),
		Mention: highlights > 0,
		Draft:   draft,
		Invite:  room.IsInvite(),
	}
}

// ownerName is the name of the exclusive tag room shows under; "" for none.
func (v unreadView) ownerName(room domain.Room) string {
	if owner := v.tagsOf(room).Owner; owner >= 0 {
		return v.tags.At(owner).Name
	}
	return ""
}

func (v unreadView) silenced(room domain.Room) bool { return v.tagsOf(room).Silenced }
