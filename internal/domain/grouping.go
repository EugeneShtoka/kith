package domain

import "slices"

// A grouping is a set of chats a person made on a network for themselves — Telegram's
// folders — which kith copies into a tag of the same name when asked, never syncing it
// after. What is copied is the chats themselves, of one account.

// Grouping is one of an account's groupings as it is now.
type Grouping struct {
	Name string
	// Rooms are the chats it holds, as kith names them.
	Rooms []RoomID
	// Left is what of it a copy cannot keep (a filter on whether a chat is read, say),
	// in words, for the person to know.
	Left []string
}

// GroupingDiff is what copying a grouping into its tag would change, among one
// account's rooms only: the tag's other rooms (another network's, another account's)
// are not the grouping's to change.
type GroupingDiff struct {
	Grouping Grouping
	// Exists is whether a tag has its name already.
	Exists bool
	// Add are the grouping's rooms the tag does not hold; Remove the account's rooms
	// the tag holds that the grouping does not.
	Add, Remove []RoomID
}

// GroupingChoice is what to do with one grouping's tag.
type GroupingChoice int

// The choices, per tag.
const (
	// KeepTags changes nothing.
	KeepTags GroupingChoice = iota
	// MergeIn adds the grouping's rooms and takes none away.
	MergeIn
	// TakeNetworks makes the account's rooms in the tag exactly the grouping's.
	TakeNetworks
)

// DiffGroupings is what copying each grouping into its tag would change, among owner's
// rooms: rooms are every room's facts, the tags judged on them as places (no state).
func DiffGroupings(groupings []Grouping, tags TagSet, rooms []RoomFacts, owner RoomOwner) []GroupingDiff {
	out := make([]GroupingDiff, 0, len(groupings))
	for _, g := range groupings {
		d := GroupingDiff{Grouping: g}
		i, exists := tags.Index(g.Name)
		d.Exists = exists
		for _, r := range rooms {
			if !owner.Owns(RoomID(r.ID)) {
				continue
			}
			held := exists && tags.HasAt(i, r, RoomState{})
			in := slices.Contains(g.Rooms, RoomID(r.ID))
			switch {
			case in && !held:
				d.Add = append(d.Add, RoomID(r.ID))
			case !in && held:
				d.Remove = append(d.Remove, RoomID(r.ID))
			}
		}
		out = append(out, d)
	}
	return out
}
