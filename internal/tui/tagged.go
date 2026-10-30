package tui

import (
	"github.com/EugeneShtoka/kith/internal/domain"
)

// What the tags make of each room: which hold it, whether it is silenced, the tag it
// shows under alone. kithd judges it with the rail (rail.go); a room the rail does not
// know is judged here.

// tagsOf is what the tags make of room, as the rail served judged it; a room the rail
// does not know is judged on its own.
func (v unreadView) tagsOf(room domain.Room) domain.RoomTags {
	if served, ok := v.served.room(room.ID); ok {
		return served.Tags(v.tags)
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
	if served, ok := v.served.room(room.ID); ok {
		return served.Owner
	}
	if owner := v.computeTags(room).Owner; owner >= 0 {
		return v.tags.At(owner).Name
	}
	return ""
}

// silenced reports whether a silent tag holds room.
func (v unreadView) silenced(room domain.Room) bool {
	if served, ok := v.served.room(room.ID); ok {
		return served.Silenced
	}
	return v.computeTags(room).Silenced
}
