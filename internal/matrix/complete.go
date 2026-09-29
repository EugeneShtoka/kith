package matrix

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// Word completion: the cache's own vocabulary first, in its order, then the installed
// frequency lists fill what is left (domain.MergeCandidates). A word you have used
// beats a commoner one you have not.

// completeSpare is how many extra candidates to ask the cache for, since short words
// are filtered out afterwards.
const completeSpare = 8

// The [complete] sources names (internal/matrix reads no config).
const (
	sourceHistory   = "history"
	sourceFrequency = "frequency"
)

// CompleteWord finishes a partly typed word from what has been said in scope, with
// rooms expanded through their upgrade chains.
func (b *InProc) CompleteWord(ctx context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error) {
	if req.Prefix == "" || req.Limit <= 0 {
		return nil, nil
	}

	var ranked []domain.WordCandidate
	if b.cache != nil && req.Wants(sourceHistory) {
		rooms, err := b.cache.RoomChains(ctx, req.RoomIDs)
		if err != nil {
			return nil, fmt.Errorf("matrix: expand completion scope: %w", err)
		}
		spaceRooms, err := b.cache.RoomChains(ctx, req.SpaceRooms)
		if err != nil {
			return nil, fmt.Errorf("matrix: expand completion space: %w", err)
		}
		scoped := req
		scoped.RoomIDs, scoped.SpaceRooms = rooms, spaceRooms
		scoped.Limit = req.Limit + completeSpare
		ranked, err = b.vocab.rank(ctx, b.cache, scoped, b.accountID(), scoped.Limit)
		if err != nil {
			return nil, fmt.Errorf("matrix: complete word: %w", err)
		}
	}
	if len(ranked) > req.Limit {
		ranked = ranked[:req.Limit]
	}
	if !req.Wants(sourceFrequency) {
		return ranked, nil
	}
	return domain.MergeCandidates(ranked, b.spell.completions(req.Prefix, req.Limit), req.Limit), nil
}

// accountID is this account's MXID, or "" before a session exists.
func (b *InProc) accountID() string {
	if b.client == nil {
		return ""
	}
	return string(b.client.UserID)
}

// completions is what the frequency list for the prefix's script offers. Nothing when
// the prefix is not one word (same tokenizer as the checker), no list is installed for
// the script, or the engine has not started yet (lists load with it).
func (s *spellcheck) completions(prefix string, limit int) []domain.WordCandidate {
	words := spell.Words(prefix)
	if len(words) != 1 {
		return nil
	}
	s.mu.Lock()
	rarity := s.rarity[words[0].Script]
	s.mu.Unlock()

	found := rarity.Prefix(prefix, limit)
	out := make([]domain.WordCandidate, 0, len(found))
	for _, c := range found {
		if !domain.LongEnough(prefix, c.Word) {
			continue
		}
		out = append(out, domain.WordCandidate{Word: c.Word, Score: c.Count})
	}
	return out
}
