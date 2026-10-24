package domain

import (
	"fmt"
	"slices"
	"strings"
)

// RoomFacts is what a list entry can match: the room, its spaces, tags and network.
type RoomFacts struct {
	ID     string
	Name   string
	Spaces []string // names of the spaces holding it
	// SpaceLabels are the rail labels of those spaces that are renamed ([[display.name]]).
	SpaceLabels []string
	Direct      bool
	Protocol    Protocol // the network: its own (Room.Network), else its space's bridge's
	Tags        []string // names of the [[tag]]s holding it, judged as a place (TagSet.Of)
	// ArchivedIn is the tag of the network archive holding it, when kith follows that
	// archive (Places.Archives); "" otherwise.
	ArchivedIn string
	// Caught is the tags a filter moved it into, released ones left out.
	Caught []string
}

// Places is what RoomFacts depend on besides the room. Every user of RoomFacts builds
// them through Facts, so a list entry means the same room everywhere.
type Places struct {
	Names map[RoomID]string // [[display.name]], by room
	Order HomeOrder         // how a room's spaces (and homes) are ranked
	Tags  TagSet
	// Archives is, per network whose archive kith follows, the tag that archive is.
	Archives map[Protocol]string
	// Caught is, per room, the tags a filter moved it into.
	Caught map[RoomID][]string
}

// Facts builds room's facts; holders are its spaces in hierarchy order (HoldersOf).
// The name is the local name, else the display name (never shortened). The network
// is Room.Network, else the first bridged holder's, else the room ID's.
func (p Places) Facts(room Room, holders []Space) RoomFacts {
	facts := RoomFacts{
		ID: string(room.ID), Name: room.DisplayName(), Direct: room.IsDirect,
		Protocol: NetworkOf(string(room.ID)),
	}
	if name := p.Names[room.ID]; name != "" {
		facts.Name = name
	}
	bridged := room.Network != ""
	if bridged {
		facts.Protocol = room.Network
	}
	names := make([]string, 0, len(holders))
	for i := range holders {
		names = append(names, holders[i].DisplayName())
		if !bridged && holders[i].Bridge.IsBridged() {
			facts.Protocol, bridged = holders[i].Bridge, true
		}
	}
	if len(names) > 0 {
		facts.Spaces = p.Order.WithManaged(holders).Sort(names)
		for _, name := range facts.Spaces {
			if label := p.Order.Label(name); label != "" {
				facts.SpaceLabels = append(facts.SpaceLabels, label)
			}
		}
	}
	if room.Archived {
		facts.ArchivedIn = p.Archives[NetworkOf(string(room.ID))]
	}
	facts.Caught = p.Caught[room.ID]
	// Last: a tag's rule matches on the facts above.
	facts.Tags = p.Tags.Of(facts)
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

// The prefixes and bare words a list entry may use (also tag: and place:).
const (
	entryRoom     = "room:"
	entrySpace    = "space:"
	entryProtocol = "protocol:"
	entryDirect   = "dm"
	entryGroup    = "group"
)

// EntryKind is how broadly one entry reaches: one room is narrower than a set.
type EntryKind int

const (
	// EntryInvalid is an entry that declares no kind — refused rather than guessed at.
	EntryInvalid EntryKind = iota
	// EntryClass names a *set* of rooms: a space, a tag, a rail row, a network, or
	// whether it is a DM.
	EntryClass
	// EntryRoom names one room, by ID or by the name it is shown under.
	EntryRoom
)

// ParseEntry reports an entry's kind without a room, so config can be validated at startup.
func ParseEntry(entry string) (EntryKind, bool) {
	entry = strings.TrimSpace(entry)
	switch {
	case entry == "":
		return EntryInvalid, false
	case strings.EqualFold(entry, entryDirect), strings.EqualFold(entry, entryGroup),
		hasPrefixFold(entry, entrySpace), hasPrefixFold(entry, entryProtocol), hasPrefixFold(entry, termTag),
		hasPrefixFold(entry, entryPlace):
		return EntryClass, true
	case hasPrefixFold(entry, entryRoom) && !BareMatrixID(entry), IsRoomID(entry):
		return EntryRoom, true
	default:
		return EntryInvalid, false
	}
}

// BareMatrixID reports a Matrix ID written as Matrix itself writes it, alone or after
// room: or thread:. kith writes one with its network, and a room or event with the
// account that sees it.
func BareMatrixID(entry string) bool {
	entry = strings.TrimSpace(entry)
	for _, prefix := range []string{entryRoom, "thread:"} {
		if hasPrefixFold(entry, prefix) {
			entry = strings.TrimSpace(entry[len(prefix):])
		}
	}
	return IsMatrixAddress(entry) || (len(entry) > 1 && entry[0] == '$') || TypedUserID(entry) != entry
}

// BareMatrixError is the refusal of a config entry that is a BareMatrixID.
func BareMatrixError(what, entry string) error {
	return fmt.Errorf("%s: %q is a Matrix ID as Matrix writes it: kith writes a room as "+
		"matrix:<account>/!…, an event as matrix:<account>/$… and a person as matrix:@…, as kith shows them",
		what, entry)
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

// EntryPrefixes are the prefixes a list entry may carry; EntryWords what else it may be.
const (
	EntryPrefixes = "room:, space:, tag:, place:, protocol:"
	EntryWords    = "a room ID, dm, group"
)

// ValidateEntries reports the first entry that declares no kind.
func ValidateEntries(what string, entries []string) error {
	for _, entry := range entries {
		if _, ok := ParseEntry(entry); !ok {
			return NeedsPrefix(what, entry, EntryPrefixes, EntryWords)
		}
	}
	return nil
}

// NeedsPrefix is the error for an entry with no prefix it may carry: which prefixes it
// may, and what else ("" for nothing) it may be instead.
func NeedsPrefix(what, entry, prefixes, words string) error {
	if BareMatrixID(entry) {
		return BareMatrixError(what, entry)
	}
	if words == "" {
		return fmt.Errorf("%s: %q needs a prefix: %s", what, entry, prefixes)
	}
	return fmt.Errorf("%s: %q needs a prefix: %s (or %s)", what, entry, prefixes, words)
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
	case hasPrefixFold(entry, termTag):
		want, _ := TagOf(entry)
		return kind, slices.ContainsFunc(f.Tags, func(tag string) bool { return strings.EqualFold(tag, want) })
	case hasPrefixFold(entry, entryPlace):
		want := strings.TrimSpace(entry[len(entryPlace):])
		return kind, f.inSpaceShownAs(want) || slices.ContainsFunc(f.Tags, func(tag string) bool { return strings.EqualFold(tag, want) })
	case hasPrefixFold(entry, entryRoom):
		// `room:` takes the displayed name or the ID.
		entry = strings.TrimSpace(entry[len(entryRoom):])
	}
	return kind, strings.EqualFold(entry, strings.TrimSpace(f.ID)) ||
		(f.Name != "" && strings.EqualFold(entry, strings.TrimSpace(f.Name)))
}

// inSpaceShownAs reports whether a space holding the room has name or shows as it.
func (f RoomFacts) inSpaceShownAs(name string) bool {
	same := func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), name) }
	return name != "" && (slices.ContainsFunc(f.Spaces, same) || slices.ContainsFunc(f.SpaceLabels, same))
}

// Names reports whether one list entry describes this room.
func (f RoomFacts) Names(entry string) bool {
	_, ok := f.Match(entry)
	return ok
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
