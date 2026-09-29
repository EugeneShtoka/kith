package domain

import "slices"

// Archive is the set of places whose unread stops counting, as place entries.
type Archive struct {
	Entries []string
}

// Archived reports whether this room's unread should stop counting.
func (a Archive) Archived(room RoomFacts) bool { return namesAny(a.Entries, room) }

// Has reports whether anything is archived at all.
func (a Archive) Has() bool { return len(a.Entries) > 0 }

// Lists reports whether this exact entry is listed (as opposed to a rule naming the
// room's space).
func (a Archive) Lists(entry string) bool { return slices.Contains(a.Entries, entry) }

// With returns the archive with one entry added or removed.
func (a Archive) With(entry string, archived bool) Archive {
	return Archive{Entries: withEntry(a.Entries, entry, archived)}
}

// Pinned is the conversations you are following right now, as place entries.
type Pinned struct {
	Entries []string
}

// Has reports whether anything is pinned.
func (p Pinned) Has() bool { return len(p.Entries) > 0 }

// Pins reports whether this room is pinned.
func (p Pinned) Pins(room RoomFacts) bool { return namesAny(p.Entries, room) }

// Lists reports whether this exact entry is pinned.
func (p Pinned) Lists(entry string) bool { return slices.Contains(p.Entries, entry) }

// With adds or removes one entry, returning the new set.
func (p Pinned) With(entry string, pinned bool) Pinned {
	return Pinned{Entries: withEntry(p.Entries, entry, pinned)}
}
