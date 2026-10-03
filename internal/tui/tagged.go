package tui

import (
	"reflect"
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// What the tags make of each room: which hold it, whether it is silenced, the
// exclusive tag it shows under, and the space-exclusive tags that take it out of
// other groupings. Every rail row asks this about every room on every draw, so it is
// kept per room in a memo shared by the Model's copies, keyed by the identity of
// everything it is computed from: the inputs are copy-on-write maps (cow.go), so any
// change to them is a new map, and a new key.

// roomTags is what the tags make of one room.
type roomTags struct {
	in        []bool // by tag index: the tag holds it
	silenced  bool   // a silent tag holds it
	owner     int    // the exclusive tag it shows under, first in config order; -1 none
	spaceExcl []int  // the space-exclusive tags holding it
}

// tagMemo is the per-room answers for one set of inputs.
type tagMemo struct {
	key  tagKey
	byID map[domain.RoomID]roomTags
}

// tagKey identifies the inputs: the tags (by revision) and each map by identity.
type tagKey struct {
	tagsRev                     uint64
	unread, drafts, facts, spam uintptr
	local                       bool
}

// identity is a map's identity; nil is 0.
func identity(m any) uintptr {
	v := reflect.ValueOf(m)
	if v.Kind() != reflect.Map || v.IsNil() {
		return 0
	}
	return v.Pointer()
}

func (v unreadView) tagKey() tagKey {
	return tagKey{
		tagsRev: v.tagsRev,
		unread:  identity(v.counts), drafts: identity(v.drafts), facts: identity(v.roomFacts),
		spam:  identity(v.spamRooms),
		local: v.local,
	}
}

// tagsOf is what the tags make of room, from the memo when it is current.
func (v unreadView) tagsOf(room domain.Room) roomTags {
	if v.tags.Len() == 0 {
		return roomTags{owner: -1}
	}
	memo := v.tagMemo
	if memo != nil {
		if key := v.tagKey(); memo.key != key || memo.byID == nil {
			memo.key, memo.byID = key, map[domain.RoomID]roomTags{}
		}
		if got, ok := memo.byID[room.ID]; ok {
			return got
		}
	}
	got := v.computeTags(room)
	if memo != nil {
		memo.byID[room.ID] = got
	}
	return got
}

// computeTags judges every tag on one room. Silence is judged on the room's own
// state; every other tag then sees a silenced room as read, so a silent
// tag never feeds back on itself.
func (v unreadView) computeTags(room domain.Room) roomTags {
	facts := v.factsOf(room)
	raw := v.rawState(room)
	n := v.tags.Len()
	out := roomTags{in: make([]bool, n), owner: -1}
	for i := range n {
		if v.tags.At(i).Silent && v.tags.HasAt(i, facts, raw) {
			out.silenced = true
			break
		}
	}
	seen := raw
	if out.silenced {
		seen.Unread, seen.Mention = false, false
	}
	for i := range n {
		tag, state := v.tags.At(i), seen
		if tag.Silent {
			state = raw
		}
		if out.in[i] = v.tags.HasAt(i, facts, state); !out.in[i] {
			continue
		}
		if tag.Exclusive && out.owner < 0 {
			out.owner = i
		}
		if tag.SpaceExclusive {
			out.spaceExcl = append(out.spaceExcl, i)
		}
	}
	return out
}

// factsOf is what a place entry can match about room.
func (v unreadView) factsOf(room domain.Room) domain.RoomFacts {
	facts, ok := v.roomFacts[room.ID]
	if !ok && v.facts != nil {
		facts = v.facts(room) // arrived since the last refresh: the slow way, once
	}
	return facts
}

// tagState is the state tag i judges room in, as computeTags judges it: the room's
// own for a silent tag; read, for any other, while a silent tag holds the room.
func (v unreadView) tagState(i int, room domain.Room) domain.RoomState {
	state := v.rawState(room)
	if !v.tags.At(i).Silent && v.silenced(room) {
		state.Unread, state.Mention = false, false
	}
	return state
}

// rawState is what a room is right now, before any tag silences it.
func (v unreadView) rawState(room domain.Room) domain.RoomState {
	_, highlights := v.count(room)
	_, draft := v.drafts[room.ID]
	return domain.RoomState{
		Unread:  v.counts[room.ID].HasUnread(v.local),
		Mention: highlights > 0,
		Draft:   draft,
		Spam:    v.isSpam(room),
		Invite:  room.IsInvite(),
	}
}

// showsInTag reports whether tag i's rail row lists room: the tag holds it and no
// other tag claims it — an exclusive tag holding it shows it alone, and a
// space-exclusive one takes it out of every tag that is not space-exclusive too.
func (v unreadView) showsInTag(i int, room domain.Room) bool {
	t := v.tagsOf(room)
	if !t.in[i] {
		return false
	}
	if t.owner >= 0 {
		return t.owner == i
	}
	return len(t.spaceExcl) == 0 || slices.Contains(t.spaceExcl, i)
}

// ownerName is the name of the exclusive tag room shows under; "" for none.
func (v unreadView) ownerName(room domain.Room) string {
	if owner := v.tagsOf(room).owner; owner >= 0 {
		return v.tags.At(owner).Name
	}
	return ""
}

// leavesMadeSpaces reports whether a space-exclusive tag holds room, taking it out of
// the spaces a person made.
func (v unreadView) leavesMadeSpaces(room domain.Room) bool {
	return len(v.tagsOf(room).spaceExcl) > 0
}

// silenced reports whether a silent tag holds room.
func (v unreadView) silenced(room domain.Room) bool { return v.tagsOf(room).silenced }
