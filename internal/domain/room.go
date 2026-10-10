// Package domain holds kith's pure chat vocabulary, shared by every network: value
// types and the logic over them (display-name resolution, sorting) with no I/O, no
// network SDK, and no TUI framework.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// RoomID identifies a Matrix room (e.g. "!abc:example.org").
type RoomID string

// Membership is our own relationship to a room.
type Membership string

// The memberships the client represents. Matrix also has "leave" and "knock";
// neither is listed, so neither is modeled.
const (
	MembershipJoin   Membership = "" // joined; the zero value, see Membership
	MembershipInvite Membership = "invite"
)

// Room is a Matrix room we are in or have been invited to, reduced to what the
// client needs to list it.
type Room struct {
	ID       RoomID
	Name     string // the room's m.room.name, if it has one; may be empty
	Topic    string // m.room.topic
	IsDirect bool   // per the user's m.direct account data
	// Members holds participant display names (excluding us, up to a small cap).
	Members    []string
	Membership Membership
	InvitedBy  string // MXID of the inviter, for an invite
	// Replacement is the m.room.tombstone replacement_room, if upgraded.
	Replacement RoomID
	// Archived is the network's own archive holding it: Telegram's Archived folder,
	// WhatsApp's archived chats. kith's tag follows it where configured (Places).
	Archived bool
	// Forum marks a forum's own room (Telegram's General topic): the forum is a space,
	// and each of its other topics a room in it.
	Forum bool
	// Deleting is whether the room is put away by deleting it rather than leaving it
	// (a private chat on a network that cannot leave one), and for whom.
	Deleting ChatDeleting
	// Network is the network behind a room another network keeps for it (a Matrix
	// room a bridge keeps: the bridge's network); "" when none is known.
	Network Protocol
}

// ChatDeleting is how a chat that cannot be left is deleted instead.
type ChatDeleting int

// The ways a chat is put away.
const (
	// ChatLeft: the room is left, not deleted (a group, a Matrix room).
	ChatLeft ChatDeleting = iota
	// ChatDeletedForMe: the chat is deleted for you only (your own saved messages).
	ChatDeletedForMe
	// ChatDeletedForEither: the chat is deleted for you, and, if you say so, for the
	// other person too (a Telegram private chat).
	ChatDeletedForEither
)

// IsInvite reports whether this is a pending invitation rather than a room we have
// joined.
func (r Room) IsInvite() bool { return r.Membership == MembershipInvite }

// DisplayName is the human-facing label for a room: its name when set,
// otherwise its ID, so a room is never rendered blank.
func (r Room) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	return string(r.ID)
}

// FormatHeroes builds a nameless room's display name from its members ("heroes"), per
// Matrix's summary-based naming; extra counts members beyond those shown.
func FormatHeroes(shown []string, extra int) string {
	switch {
	case len(shown) == 0:
		return ""
	case extra > 0:
		return strings.Join(shown, ", ") + " and " + othersPhrase(extra)
	case len(shown) == 1:
		return shown[0]
	default:
		return strings.Join(shown[:len(shown)-1], ", ") + " and " + shown[len(shown)-1]
	}
}

func othersPhrase(n int) string {
	if n == 1 {
		return "1 other"
	}
	return fmt.Sprintf("%d others", n)
}

// SortRooms orders rooms in place, case-insensitively by display name.
func SortRooms(rooms []Room) {
	sort.SliceStable(rooms, func(i, j int) bool {
		return strings.ToLower(rooms[i].DisplayName()) < strings.ToLower(rooms[j].DisplayName())
	})
}

// NewRoom is a room to be created.
type NewRoom struct {
	Name      string
	Space     bool     // create an m.space
	Encrypted bool     // E2EE from creation
	Public    bool     // anyone may join
	Parent    SpaceID  // file into this space once created
	Invite    []string // invited at creation: people's IDs on the network it is made on
	// Direct sets is_direct and records the room in our m.direct.
	Direct bool
	// On is the account it is made on: AccountRooms of a network other than Matrix's,
	// which makes it as Kind says; empty for Matrix, which reads the fields above.
	On   RoomOwner
	Kind ChatKind
}

// ChatKind is what a chat on a network other than Matrix is made as.
type ChatKind int

// The kinds of chat a network makes.
const (
	// ChatGroup is a group: a Telegram supergroup, a WhatsApp group.
	ChatGroup ChatKind = iota + 1
	// ChatForum is a Telegram group with topics.
	ChatForum
	// ChatChannel is a Telegram broadcast channel, or a Slack public channel.
	ChatChannel
	// ChatPrivateChannel is a Slack private channel.
	ChatPrivateChannel
	// ChatCommunity is a WhatsApp community: groups under one parent.
	ChatCommunity
)
