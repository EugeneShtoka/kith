package domain

import (
	"sort"
	"strings"
)

// SpaceID identifies a Matrix space, which is itself a room (type m.space).
type SpaceID string

// Space is a Matrix space: a room-of-rooms. Children holds the IDs of its direct child
// rooms (sub-spaces excluded), used to filter the room list to the space in the rail.
type Space struct {
	ID       SpaceID
	Name     string
	Children []RoomID
	// Bridge is the network whose bridge owns this space, and ProtocolMatrix for one a
	// person keeps.
	Bridge Protocol
	// Keeper is somebody other than you who is inside this space and inside your rooms
	// at large — which is what a bridge looks like from the outside, and what a person
	// does not.
	Keeper string
	// Original marks a space that at least one room names as its canonical parent — the
	// space that room came from, as opposed to one it was also filed into.
	Original bool
	// Leaving is what leaving the space from the rail does.
	Leaving SpaceLeaving
	// LeaveBy is the room whose leaving leaves the space and all of its rooms with it (a
	// Telegram forum's chat: its topics go with it), for LeftByRoom; "" otherwise.
	LeaveBy RoomID
}

// SpaceLeaving is how a space is left, which the network decides.
type SpaceLeaving int

// The ways a space is left.
const (
	// NotLeft: an account's own space (leaving it would be signing out), or a bridge's
	// view of one.
	NotLeft SpaceLeaving = iota
	// LeftByRoom: leaving one room (LeaveBy) leaves the space and its rooms (a Telegram
	// forum).
	LeftByRoom
	// LeftWithRooms: the space is left with every room inside it (a WhatsApp
	// community: its groups go too).
	LeftWithRooms
	// LeftAlone: the space is a membership of its own, and so is each room inside it,
	// which may stay (a Matrix space).
	LeftAlone
)

// Leavable reports whether the space can be left at all.
func (s Space) Leavable() bool { return s.Leaving != NotLeft }

// KeeperReach is how many of your rooms a space's other member has to be inside before
// it is taken for a bridge rather than a person.
const KeeperReach = 5

// Managed reports whether something other than you decides what is in this space — a
// bridge that keeps it, or a room that calls it home.
func (s Space) Managed() bool { return s.Keeper != "" || s.Original }

// IsBridged reports whether a bridge created this space, and therefore whether it is a
// place this client should offer to file rooms into.
func (s Space) IsBridged() bool { return s.Bridge != "" && s.Bridge.IsBridged() }

// DisplayName is the human-facing label for a space: its name when set,
// otherwise its ID, so a space is never rendered blank.
func (s Space) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return string(s.ID)
}

// SortSpaces orders spaces in place, case-insensitively by display name.
func SortSpaces(spaces []Space) {
	sort.SliceStable(spaces, func(i, j int) bool {
		return strings.ToLower(spaces[i].DisplayName()) < strings.ToLower(spaces[j].DisplayName())
	})
}
