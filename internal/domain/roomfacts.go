package domain

import (
	"fmt"
	"slices"
	"strings"
)

// RoomFacts is what a list entry can be about: the room itself, where it sits, and what
// network it is on.
type RoomFacts struct {
	ID       string
	Name     string
	Spaces   []string // names of the spaces holding it
	Direct   bool
	Protocol Protocol // the network, from the bridge that owns its space
	Pinned   bool
}

// Places is what a room's facts depend on besides the room itself: the names the
// person gave rooms, the order they put spaces in, and what they pinned. Every place
// that decides by RoomFacts — notification rules, the assistant's and the agent's
// scopes, codes, tracked words, the archive — builds them through Facts, so a list
// entry means the same room everywhere.
type Places struct {
	Names    map[RoomID]string // [[display.name]], by room
	Priority []string          // space_priority
	Pinned   Pinned
}

// Facts is room as a list entry matches it. holders are the spaces holding it, in
// hierarchy order (HoldersOf). The name is the one the person gave the room, else
// its own display name, never a label shortened for the room list; the spaces are
// ordered by priority; the network is the first bridged holder's, else the one the
// room's ID names.
func (p Places) Facts(room Room, holders []Space) RoomFacts {
	facts := RoomFacts{
		ID: string(room.ID), Name: room.DisplayName(), Direct: room.IsDirect,
		Protocol: NetworkOf(string(room.ID)),
	}
	if name := p.Names[room.ID]; name != "" {
		facts.Name = name
	}
	bridged := false
	names := make([]string, 0, len(holders))
	for i := range holders {
		names = append(names, holders[i].DisplayName())
		if !bridged && holders[i].Bridge.IsBridged() {
			facts.Protocol, bridged = holders[i].Bridge, true
		}
	}
	if len(names) > 0 {
		facts.Spaces = OrderSpaces(names, p.Priority)
	}
	// Last: pin entries match on the facts above, and cannot themselves say `pinned`.
	facts.Pinned = p.Pinned.Pins(facts)
	return facts
}

// HoldersOf is the spaces holding roomID, in hierarchy order.
func HoldersOf(roomID RoomID, spaces []Space) []Space {
	var out []Space
	for i := range spaces {
		if slices.Contains(spaces[i].Children, roomID) {
			out = append(out, spaces[i])
		}
	}
	return out
}

// The prefixes and bare words a list entry may use.
const (
	entryRoom     = "room:"
	entrySpace    = "space:"
	entryProtocol = "protocol:"
	entryDirect   = "dm"
	entryGroup    = "group"
	entryPinned   = "pinned"
	// roomSigil marks a Matrix room ID, which is a complete entry on its own; so is
	// any other network's room ID (IsRoomID).
	roomSigil = "!"
)

// EntryKind is how broadly one entry reaches: one room is narrower than a set.
type EntryKind int

const (
	// EntryInvalid is an entry that declares no kind — refused rather than guessed at.
	EntryInvalid EntryKind = iota
	// EntryClass names a *set* of rooms: a space, a network, or whether it is a DM.
	EntryClass
	// EntryRoom names one room, by ID or by the name it is shown under.
	EntryRoom
)

// ParseEntry reports what kind of thing an entry names, without a room to compare it
// against (so config can be validated at startup).
func ParseEntry(entry string) (EntryKind, bool) {
	entry = strings.TrimSpace(entry)
	switch {
	case entry == "":
		return EntryInvalid, false
	case strings.EqualFold(entry, entryDirect), strings.EqualFold(entry, entryGroup),
		strings.EqualFold(entry, entryPinned),
		hasPrefixFold(entry, entrySpace), hasPrefixFold(entry, entryProtocol):
		return EntryClass, true
	case hasPrefixFold(entry, entryRoom), strings.HasPrefix(entry, roomSigil), IsRoomID(entry):
		return EntryRoom, true
	default:
		return EntryInvalid, false
	}
}

// SpaceEntry spells a space as a list entry: `space:<name>`.
func SpaceEntry(name string) string { return entrySpace + name }

// SpaceOf is the inverse of SpaceEntry; false when the entry names something else.
func SpaceOf(entry string) (string, bool) {
	entry = strings.TrimSpace(entry)
	if !hasPrefixFold(entry, entrySpace) {
		return "", false
	}
	return strings.TrimSpace(entry[len(entrySpace):]), true
}

// ValidateEntries reports the first entry that declares no kind.
func ValidateEntries(what string, entries []string) error {
	for _, entry := range entries {
		if _, ok := ParseEntry(entry); !ok {
			return fmt.Errorf("%s: %q names nothing — write a room ID (!abc:server), "+
				"room:<name>, space:<name>, protocol:<network>, dm, group or pinned", what, entry)
		}
	}
	return nil
}

// IsPinnedEntry reports whether entry is the `pinned` class.
func IsPinnedEntry(entry string) bool {
	return strings.EqualFold(strings.TrimSpace(entry), entryPinned)
}

// Match reports whether one entry describes this room, and how broadly it reaches.
func (f RoomFacts) Match(entry string) (EntryKind, bool) {
	kind, ok := ParseEntry(entry)
	if !ok {
		return EntryInvalid, false
	}
	entry = strings.TrimSpace(entry)
	switch {
	case strings.EqualFold(entry, entryDirect):
		return kind, f.Direct
	case strings.EqualFold(entry, entryGroup):
		return kind, !f.Direct
	case strings.EqualFold(entry, entryPinned):
		// A class, so a `pinned` rule is narrower than global do-not-disturb.
		return kind, f.Pinned
	case hasPrefixFold(entry, entrySpace):
		want := strings.TrimSpace(entry[len(entrySpace):])
		for _, space := range f.Spaces {
			if strings.EqualFold(strings.TrimSpace(space), want) {
				return kind, true
			}
		}
		return kind, false
	case hasPrefixFold(entry, entryProtocol):
		return kind, strings.EqualFold(f.Protocol.String(), strings.TrimSpace(entry[len(entryProtocol):]))
	case hasPrefixFold(entry, entryRoom):
		// `room:` takes the displayed name or the ID.
		entry = strings.TrimSpace(entry[len(entryRoom):])
	}
	return kind, strings.EqualFold(entry, strings.TrimSpace(f.ID)) ||
		(f.Name != "" && strings.EqualFold(entry, strings.TrimSpace(f.Name)))
}

// Names reports whether one list entry describes this room.
func (f RoomFacts) Names(entry string) bool {
	_, ok := f.Match(entry)
	return ok
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
