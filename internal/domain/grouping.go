package domain

import "slices"

// Grouping is a network's chat folder (e.g. Telegram's) for one account, which kith
// copies into a same-named tag on request, without syncing it after.
type Grouping struct {
	Name  string
	Rooms []RoomID
	// Left describes, in words, what a copy cannot keep (e.g. a read-state filter).
	Left []string
}

// GroupingDiff is what copying a grouping into its tag would change, among that
// account's rooms only.
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
// rooms; tags are judged on facts alone (no state).
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
