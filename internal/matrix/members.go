package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// mentionRankLimit bounds each ranking rung (recent speakers, mention history).
const mentionRankLimit = 32

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
		return capMembers(members, limit), nil
	}
	byID := make(map[string]domain.Member, len(members))
	for _, member := range members {
		byID[member.UserID] = member
	}

	speakers, err := b.cache.RecentSpeakers(ctx, roomID, mentionRankLimit)
	if err != nil {
		return nil, fmt.Errorf("matrix: rank recent speakers: %w", err)
	}
	mentioned, err := b.cache.FrequentMentions(ctx, roomID, mentionRankLimit)
	if err != nil {
		return nil, fmt.Errorf("matrix: rank mention history: %w", err)
	}
	return capMembers(rankMembers(members, byID, speakers, mentioned), limit), nil
}

// rankMembers lays out the rungs in order, skipping duplicates and non-members.
func rankMembers(all []domain.Member, byID map[string]domain.Member, rungs ...[]string) []domain.Member {
	placed := make(map[string]bool, len(all))
	out := make([]domain.Member, 0, len(all))
	for _, rung := range rungs {
		for _, userID := range rung {
			member, present := byID[userID]
			if !present || placed[userID] {
				continue
			}
			placed[userID] = true
			out = append(out, member)
		}
	}
	// Everyone else, alphabetically — `all` arrives sorted.
	for _, member := range all {
		if !placed[member.UserID] {
			out = append(out, member)
		}
	}
	return out
}

// capMembers trims a ranked list to limit (0 or less = no limit).
func capMembers(members []domain.Member, limit int) []domain.Member {
	if limit > 0 && len(members) > limit {
		return members[:limit]
	}
	return members
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
