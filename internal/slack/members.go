package slack

import (
	"context"
	"fmt"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// membersPage is how many members one conversations.members page asks for.
const membersPage = 1000

// Members is a room's members, from the cache.
func (a *Adapter) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	if a.cache == nil {
		return nil, nil
	}
	members, err := a.cache.Members(ctx, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("slack: read members of %s: %w", roomID, err)
	}
	return members, nil
}

// RefreshMembers asks Slack who is in a conversation, names them, and caches them.
func (a *Adapter) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	w, channel, err := a.conversation(roomID)
	if err != nil {
		return a.Members(ctx, roomID, 0) // what is known, while the workspace is not connected
	}
	var users []string
	for cursor := ""; ; {
		var page []string
		err := waitingOut(ctx, func() (err error) {
			page, cursor, err = w.client.GetUsersInConversationContext(ctx, &slackgo.GetUsersInConversationParameters{
				ChannelID: channel, Cursor: cursor, Limit: membersPage,
			})
			if err != nil {
				return fmt.Errorf("slack: members of %s: %w", roomID, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		users = append(users, page...)
		if cursor == "" {
			break
		}
	}
	a.learnPeople(ctx, w, users)
	n := w.names()
	members := make([]domain.Member, 0, len(users))
	for _, u := range users {
		members = append(members, domain.Member{UserID: personID(w.creds.Team, u), DisplayName: n.user(u)})
	}
	if a.cache != nil {
		if err := a.cache.SaveMembers(ctx, roomID, members); err != nil {
			return nil, fmt.Errorf("slack: cache members of %s: %w", roomID, err)
		}
	}
	return a.Members(ctx, roomID, 0)
}

// MentionCandidates orders a room's members for the mention dropdown, as the other
// networks' are: recent speakers, the most mentioned, then everyone alphabetically.
func (a *Adapter) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := a.Members(ctx, roomID, 0)
	if err != nil || len(members) == 0 || a.cache == nil {
		return domain.RankMembers(members, nil, nil, limit), err
	}
	speakers, err := a.cache.RecentSpeakers(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("slack: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("slack: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}
