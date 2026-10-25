// Package domain holds kith's pure chat vocabulary, shared by every network: value
// types and the logic over them (display-name resolution, sorting) with no I/O, no
// network SDK, and no TUI framework.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// RoomID identifies a room as one account sees it (e.g. "matrix:<account>/!abc:example.org").
type RoomID string

// Membership is our own relationship to a room.
type Membership string

// Memberships the client models; Matrix's "leave" and "knock" rooms are never listed.
const (
	MembershipJoin   Membership = "" // joined; the zero value, see Membership
	MembershipInvite Membership = "invite"
)

// Room is a joined or invited room, reduced to what the room list needs.
type Room struct {
	ID       RoomID
	Name     string // the room's m.room.name, if it has one; may be empty
	Topic    string // m.room.topic
	IsDirect bool   // per the user's m.direct account data
	// Members holds participant display names (excluding us, up to a small cap).
	Members    []string
	Membership Membership
	InvitedBy  string // ID of the inviter, for an invite
	// Replacement is the m.room.tombstone replacement_room, if upgraded.
	Replacement RoomID
	// Archived is set by the network's own archive (Telegram's Archived folder,
	// WhatsApp's archived chats); kith's tag can follow it (Places).
	Archived bool
	// Forum marks a forum's own room (Telegram's General topic): the forum is a space,
	// and each of its other topics a room in it.
	Forum bool
	// Deleting is whether the room is deleted rather than left (a private chat on a
	// network that cannot leave one), and for whom.
	Deleting ChatDeleting
	// Network is the bridged network behind a Matrix room; "" when unknown.
	Network Protocol
	// Broadcasts are the room-wide mentions the room takes, as typed ("@here"); the
	// network sends them as such.
	Broadcasts []string
	// SMS marks a chat whose messages go out as SMS or MMS, through a phone's carrier
	// (Google Messages), rather than over the internet.
	SMS bool
	// ReadOnly marks a room this account may read but not post in (an announcements
	// room, a bridge's status room): the composer says so instead of taking a message.
	ReadOnly bool
	// Handle is the public @username that names this room on its network, where the
	// network has one (Telegram public chats, channels). Lowercase, without "@". Empty
	// for a private chat.
	Handle string
}

// ChatDeleting is how a chat that cannot be left is deleted instead.
type ChatDeleting int

// The ways a chat is put away.
const (
	// ChatLeft: the room is left, not deleted (a group, a Matrix room).
	ChatLeft ChatDeleting = iota
	// ChatDeletedForMe: the chat is deleted for you only (your own saved messages).
	ChatDeletedForMe
	// ChatDeletedForEither: deleted for you, optionally for the other person too
	// (a Telegram private chat).
	ChatDeletedForEither
)

// IsInvite reports whether this is a pending invitation.
func (r Room) IsInvite() bool { return r.Membership == MembershipInvite }

// DisplayName is the room's name, else its ID, so it is never blank.
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
	// On is the non-Matrix account to create it on (AccountRooms), as Kind says;
	// empty for Matrix, which uses the fields above.
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

// ChatOffer is one kind of chat an account can make, as its network describes it.
type ChatOffer struct {
	// On is the account it is made on.
	On RoomOwner
	// Account is how the account reads, to tell a network's accounts apart: a number,
	// a workspace's name.
	Account string
	Kind    ChatKind
	Label   string
	Detail  string
	// MinMembers is how many others must be in it from the start.
	MinMembers int
}
