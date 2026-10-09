package slack

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A workspace's conversations are its rooms: the public and private channels the
// person is in, their DMs and their group DMs; archived ones are left out. The
// workspace itself is one space holding them all, so it reads as a unit in the rail;
// Slack's sidebar sections are the person's own arrangement and not in the API.

// roomID is a Slack conversation as one workspace sees it.
func roomID(team, channel string) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolSlack, team, channel))
}

// personID is a Slack user: user IDs are unique within a workspace only.
func personID(team, user string) string {
	return domain.NativePerson(domain.ProtocolSlack, team+"."+user)
}

// workspaceSpaceID is the space a workspace is: its team ID within its own account.
func workspaceSpaceID(team string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolSlack, team, team))
}

// userName is how a person reads in Slack: the display name they chose, else their
// full name, else their handle.
func userName(u slackgo.User) string {
	for _, name := range []string{u.Profile.DisplayName, u.Profile.RealName, u.RealName, u.Name} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

// groupDMName is a group DM's name from Slack's own for it, "mpdm-dana--sam--lee-1":
// the handles of the people in it but the person's own, self.
func groupDMName(name, self string) string {
	name = strings.TrimPrefix(name, "mpdm-")
	if i := strings.LastIndex(name, "-"); i > 0 {
		name = name[:i] // the trailing counter
	}
	handles := strings.Split(name, "--")
	if others := slices.DeleteFunc(slices.Clone(handles), func(h string) bool { return h == self }); len(others) > 0 {
		handles = others
	}
	return strings.Join(handles, ", ")
}

// slackOwn names Slack's own senders, which users.info does not answer for.
var slackOwn = map[string]string{"USLACKBOT": "Slackbot", "USLACK": "Slack"}

// listing is what one workspace's listing caches.
type listing struct {
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	space   domain.Space
}

// self is the person signed in: their user ID and handle in the workspace.
type self struct{ user, handle string }

// listed turns a workspace's conversations into rooms, with a DM's other people as its
// members, and the workspace into the space holding them. groups is who is in each
// group DM, by its ID, as far as Slack said; names is what is known of the people,
// by user ID.
func listed(team, teamName string, me self, conversations []slackgo.Channel, groups map[string][]string, names map[string]string) listing {
	l := listing{
		members: map[domain.RoomID][]domain.Member{},
		space: domain.Space{
			ID: workspaceSpaceID(team), Name: teamName, Bridge: domain.ProtocolSlack,
			// Every channel's home: it is where the room belongs, not a space to file into.
			// Left by signing the account out (SignOut).
			Original: true, Leaving: domain.LeftBySigningOut,
		},
	}
	for i := range conversations {
		c := conversations[i]
		if c.IsArchived || c.IsUserDeleted {
			continue
		}
		room := domain.Room{ID: roomID(team, c.ID), Topic: c.Topic.Value, Membership: domain.MembershipJoin}
		switch {
		case c.IsIM:
			room.IsDirect = true
			room.Name = cmpOr(names[c.User], slackOwn[c.User])
			l.members[room.ID] = []domain.Member{{UserID: personID(team, c.User), DisplayName: room.Name}}
		case c.IsMpIM:
			room.Name = groupDMName(c.Name, me.handle)
			if others := slices.DeleteFunc(slices.Clone(groups[c.ID]), func(u string) bool { return u == me.user }); len(others) > 0 {
				var people []string
				members := make([]domain.Member, 0, len(others))
				for _, u := range others {
					people = append(people, names[u])
					members = append(members, domain.Member{UserID: personID(team, u), DisplayName: names[u]})
				}
				// By the names its messages show; by handles while one is unknown.
				if !slices.Contains(people, "") {
					room.Name = strings.Join(people, ", ")
				}
				l.members[room.ID] = members
			}
		default:
			room.Name = c.Name
		}
		l.rooms = append(l.rooms, room)
		l.space.Children = append(l.space.Children, room.ID)
	}
	domain.SortRooms(l.rooms)
	slices.Sort(l.space.Children)
	return l
}

// groupDMs is a workspace's group DMs, by ID.
func groupDMs(conversations []slackgo.Channel) []string {
	var ids []string
	for i := range conversations {
		if conversations[i].IsMpIM {
			ids = append(ids, conversations[i].ID)
		}
	}
	return ids
}

// dmPartners is the people a workspace's DMs are with.
func dmPartners(conversations []slackgo.Channel) []string {
	var users []string
	for i := range conversations {
		if c := conversations[i]; c.IsIM && c.User != "" && !slices.Contains(users, c.User) {
			users = append(users, c.User)
		}
	}
	return users
}

// errOtherWorkspace is a sign-in that belongs to a workspace the account does not name.
var errOtherWorkspace = errors.New("those credentials sign in to another workspace")

// sameWorkspace checks a sign-in is to the account's workspace, which the account
// names by its team ID ("T0123456789") or its address ("acme"): Slack's answer carries
// both, the address as "https://acme.slack.com/".
func sameWorkspace(account Account, signedInURL, team string) error {
	if strings.HasPrefix(account.Workspace, "T") && strings.ToUpper(account.Workspace) == account.Workspace {
		if team != account.Workspace {
			return fmt.Errorf("%w: %s, not %s as [[slack.account]] %q says — sign in to that workspace, "+
				"or change its workspace", errOtherWorkspace, team, account.Workspace, account.Name)
		}
		return nil
	}
	u, err := url.Parse(signedInURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("slack answered with no workspace address (%q)", signedInURL)
	}
	got := strings.TrimSuffix(strings.ToLower(u.Host), ".slack.com")
	if got != account.Workspace {
		return fmt.Errorf("%w: %s, not %s as [[slack.account]] %q says — sign in to %s.slack.com, "+
			"or change its workspace", errOtherWorkspace, got, account.Workspace, account.Name, account.Workspace)
	}
	return nil
}
