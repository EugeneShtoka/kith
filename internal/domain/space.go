package domain

import (
	"sort"
	"strings"
)

// SpaceID identifies a Matrix space, which is itself a room (type m.space).
type SpaceID string

// Space is a Matrix space. Children are its direct child rooms, sub-spaces excluded.
type Space struct {
	ID       SpaceID
	Name     string
	Children []RoomID
	// Bridge is the network whose bridge owns this space; ProtocolMatrix for a person's.
	Bridge Protocol
	// Keeper is another member who is also in many of your rooms (KeeperReach), which
	// marks a bridge rather than a person.
	Keeper string
	// Original marks a space that at least one room names as its canonical parent.
	Original bool
	// Leaving is what leaving the space from the rail does.
	Leaving SpaceLeaving
	// LeaveBy is, for LeftByRoom, the room whose leaving leaves the whole space (a
	// Telegram forum's chat); "" otherwise.
	LeaveBy RoomID
}

// SpaceLeaving is how the network lets a space be left.
type SpaceLeaving int

// The ways a space is left.
const (
	// NotLeft: a bridge's own space for an account; signing out is the bridge's job.
	NotLeft SpaceLeaving = iota
	// LeftByRoom: leaving one room (LeaveBy) leaves the space and its rooms (a Telegram
	// forum).
	LeftByRoom
	// LeftWithRooms: the space is left with every room inside it (a WhatsApp
	// community: its groups go too).
	LeftWithRooms
	// LeftAlone: the space is left alone; its rooms may stay (a Matrix space).
	LeftAlone
	// LeftBySigningOut: the space is an account (Telegram, WhatsApp, a Slack workspace),
	// left by signing out.
	LeftBySigningOut
	// LeftWhole: the space and its rooms are one chat, left together (a bridged
	// Telegram forum: the bridge leaves the whole forum for any topic).
	LeftWhole
)

// Leavable reports whether the space can be left at all.
func (s Space) Leavable() bool { return s.Leaving != NotLeft }

// Whole reports whether the space and its rooms are one chat on the network (a
// forum and its topics), so a room inside is left only with all of it.
func (s Space) Whole() bool { return s.Leaving == LeftByRoom || s.Leaving == LeftWhole }

// KeeperReach is how many of your rooms a space member must share to count as a bridge.
const KeeperReach = 5

// Managed reports whether a bridge keeps this space or a room calls it home.
func (s Space) Managed() bool { return s.Keeper != "" || s.Original }

// IsBridged reports whether a bridge created this space.
func (s Space) IsBridged() bool { return s.Bridge != "" && s.Bridge.IsBridged() }

// DisplayName is the space's name, else its ID, so it is never blank.
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
