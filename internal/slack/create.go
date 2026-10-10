package slack

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A channel is made in one workspace, public or private, and its members invited
// after. Slack names a channel in lower case without spaces, so the name is written
// that way.

// channelNameMost is the longest channel name Slack takes.
const channelNameMost = 80

// CreateRoom makes a channel in the workspace spec.On names.
func (a *Adapter) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	team := domain.ParseID(string(spec.On)).Account
	var w *workspace
	for _, c := range a.connected() {
		if c.creds.Team == team {
			w = c
		}
	}
	if w == nil {
		return "", fmt.Errorf("slack: workspace %s is not connected", team)
	}
	if spec.Kind != domain.ChatChannel && spec.Kind != domain.ChatPrivateChannel {
		return "", fmt.Errorf("slack: makes channels, not kind %d", spec.Kind)
	}
	name := channelName(spec.Name)
	if name == "" {
		return "", fmt.Errorf("slack: %q leaves nothing a channel can be named", spec.Name)
	}
	ch, err := w.client.CreateConversationContext(ctx, slackgo.CreateConversationParams{
		ChannelName: name, IsPrivate: spec.Kind == domain.ChatPrivateChannel, TeamID: team,
	})
	if err != nil {
		return "", fmt.Errorf("slack: create #%s: %w", name, err)
	}
	room := roomID(team, ch.ID)
	var users, unknown []string
	for _, person := range spec.Invite {
		native := domain.ParseID(person).Native
		owner, user, ok := strings.Cut(native, ".")
		if domain.NetworkOf(person) != domain.ProtocolSlack || !ok || owner != team {
			unknown = append(unknown, person)
			continue
		}
		users = append(users, user)
	}
	if len(users) > 0 {
		if _, err := w.client.InviteUsersToConversationContext(ctx, ch.ID, users...); err != nil {
			return room, fmt.Errorf("slack: #%s was made, but its members were not added: %w", name, err)
		}
	}
	if len(unknown) > 0 {
		return room, fmt.Errorf("%s could not be added: not in this workspace", strings.Join(unknown, ", "))
	}
	return room, nil
}

// channelName is a name as Slack takes a channel's: lower case, a hyphen for each run of
// anything but a letter, a digit, a hyphen or an underscore, at most channelNameMost.
func channelName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if runes := []rune(out); len(runes) > channelNameMost {
		out = strings.TrimRight(string(runes[:channelNameMost]), "-")
	}
	return out
}
