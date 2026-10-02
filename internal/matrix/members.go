package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Members returns a room's cached members (live without a cache).
func (b *InProc) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	if b.cache == nil {
		return b.RefreshMembers(ctx, roomID)
	}
	members, err := b.cache.Members(ctx, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("matrix: read cached members: %w", err)
	}
	return members, nil
}

// RefreshMembers fetches a room's membership, replaces the cached list and refreshes
// the sender-name memo.
func (b *InProc) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	seen := b.names.seen(roomID)
	resp, err := b.client.JoinedMembers(ctx, id.RoomID(roomID))
	if err != nil {
		return nil, fmt.Errorf("matrix: joined members of %s: %w", roomID, err)
	}
	members := make([]domain.Member, 0, len(resp.Joined))
	names := make(map[string]string, len(resp.Joined))
	for userID, member := range resp.Joined {
		display := member.DisplayName
		members = append(members, domain.Member{UserID: string(userID), DisplayName: display})
		if display != "" {
			names[string(userID)] = display
		}
	}
	domain.SortMembers(members)

	b.names.putAt(roomID, seen, names)

	if b.cache != nil {
		if err := b.cache.SaveMembers(ctx, roomID, members); err != nil {
			return nil, fmt.Errorf("matrix: cache members: %w", err)
		}
	}
	return members, nil
}

// MentionCandidates orders a room's members for the mention dropdown: recent
// speakers, then most mentioned, then everyone else alphabetically. Ordering only;
// the caller filters by what was typed.
func (b *InProc) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := b.Members(ctx, roomID, 0)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 || b.cache == nil {
		return domain.RankMembers(members, nil, nil, limit), nil
	}
	speakers, err := b.cache.RecentSpeakers(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("matrix: rank recent speakers: %w", err)
	}
	mentioned, err := b.cache.FrequentMentions(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("matrix: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// directCandidatePool is how many talkers are ranked before excluding existing DMs
// (the people you message most mostly already have one).
const directCandidatePool = 400

// DirectCandidates ranks people to start a DM with: SearchSenders' ranking minus
// existing DM peers and ourselves (only this side knows m.direct).
func (b *InProc) DirectCandidates(ctx context.Context, limit int) ([]domain.Member, error) {
	if b.cache == nil {
		return nil, nil
	}
	talkers, err := b.cache.SearchSenders(ctx, domain.EveryRoom(), directCandidatePool)
	if err != nil {
		return nil, fmt.Errorf("matrix: direct candidates: %w", err)
	}
	taken := map[string]bool{string(b.client.UserID): true}
	peers, _ := b.directPeers(ctx) // unread: no peers to add
	for _, peer := range peers {
		taken[string(peer)] = true
	}
	out := make([]domain.Member, 0, min(limit, len(talkers)))
	for _, member := range talkers {
		if taken[member.UserID] {
			continue
		}
		out = append(out, member)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}
