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

// A workspace's rooms are the channels, DMs and group DMs the person is in, archived
// ones left out. The workspace is one space holding them all; Slack's sidebar sections
// are not in the API.

// roomID is a conversation's ID within one workspace.
func roomID(team, channel string) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolSlack, team, channel))
}

// personID is a Slack user's ID; user IDs are unique only within a workspace.
func personID(team, user string) string {
	return domain.NativePerson(domain.ProtocolSlack, team+"."+user)
}

// workspaceSpaceID is a workspace's space: its team ID within its own account.
func workspaceSpaceID(team string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolSlack, team, team))
}

// userName is a user's display name, else full name, else handle.
func userName(u slackgo.User) string {
	for _, name := range []string{u.Profile.DisplayName, u.Profile.RealName, u.RealName, u.Name} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

// groupDMName names a group DM from Slack's "mpdm-dana--sam--lee-1": the members'
// handles except self's.
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

type listing struct {
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	space   domain.Space
	// general is the workspace's general channel, "" when not listed.
	general string
}

// self is the signed-in person's user ID and handle.
type self struct{ user, handle string }

// listed turns conversations into rooms (a DM's other people as members) and the
// workspace into their space. groups is each group DM's members as far as known; names
// is known users' names by ID.
func listed(team, teamName string, me self, conversations []slackgo.Channel, groups map[string][]string, names map[string]string) listing {
	l := listing{
		members: map[domain.RoomID][]domain.Member{},
		space: domain.Space{
			ID: workspaceSpaceID(team), Name: teamName, Bridge: domain.ProtocolSlack,
			// Every channel's home, not a space to file into; left only by SignOut.
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
		if c.IsGeneral {
			l.general = c.ID
		}
		room.Broadcasts = broadcasts(room.IsDirect, c.IsGeneral)
		l.rooms = append(l.rooms, room)
		l.space.Children = append(l.space.Children, room.ID)
	}
	domain.SortRooms(l.rooms)
	slices.Sort(l.space.Children)
	return l
}

// broadcasts is what a conversation takes beside its people: @here (those active) and
// @channel (all) in any but a DM, @everyone (the whole workspace) only in the general
// channel.
func broadcasts(direct, general bool) []string {
	switch {
	case direct:
		return nil
	case general:
		return []string{"@here", "@channel", "@everyone"}
	}
	return []string{"@here", "@channel"}
}

// groupDMs is the group DMs' IDs.
func groupDMs(conversations []slackgo.Channel) []string {
	var ids []string
	for i := range conversations {
		if conversations[i].IsMpIM {
			ids = append(ids, conversations[i].ID)
		}
	}
	return ids
}

// dmPartners is the users the DMs are with.
func dmPartners(conversations []slackgo.Channel) []string {
	var users []string
	for i := range conversations {
		if c := conversations[i]; c.IsIM && c.User != "" && !slices.Contains(users, c.User) {
			users = append(users, c.User)
		}
	}
	return users
}

// errOtherWorkspace is a sign-in to a workspace other than the account's.
var errOtherWorkspace = errors.New("those credentials sign in to another workspace")

// sameWorkspace checks a sign-in matches the account's workspace, given as team ID
// ("T0123456789") or address ("acme"); Slack answers with both, the address as
// "https://acme.slack.com/".
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
