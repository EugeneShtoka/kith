package local

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// Word completion: the cache's vocabulary first, in its order, then installed
// frequency lists (domain.MergeCandidates). A word you have used beats a commoner one.

// completeSpare is extra candidates asked of the cache, since short words are filtered
// out afterwards.
const completeSpare = 8

// The [complete] source names (internal/local reads no config).
const (
	sourceHistory   = "history"
	sourceFrequency = "frequency"
)

// CompleteWord finishes a partly typed word from what was said in scope, rooms expanded
// through their upgrade chains.
func (s *Service) CompleteWord(ctx context.Context, req domain.CompleteRequest) ([]domain.WordCandidate, error) {
	if req.Prefix == "" || req.Limit <= 0 {
		return nil, nil
	}

	var ranked []domain.WordCandidate
	if s.cache != nil && req.Wants(sourceHistory) {
		rooms, err := s.cache.RoomChains(ctx, req.RoomIDs)
		if err != nil {
			return nil, fmt.Errorf("local: expand completion scope: %w", err)
		}
		spaceRooms, err := s.cache.RoomChains(ctx, req.SpaceRooms)
		if err != nil {
			return nil, fmt.Errorf("local: expand completion space: %w", err)
		}
		scoped := req
		scoped.RoomIDs, scoped.SpaceRooms = rooms, spaceRooms
		scoped.Limit = req.Limit + completeSpare
		ranked, err = s.vocab.rank(ctx, s.cache, scoped, s.me(), scoped.Limit)
		if err != nil {
			return nil, fmt.Errorf("local: complete word: %w", err)
		}
	}
	if len(ranked) > req.Limit {
		ranked = ranked[:req.Limit]
	}
	if !req.Wants(sourceFrequency) {
		return ranked, nil
	}
	return domain.MergeCandidates(ranked, s.spell.completions(req.Prefix, req.Limit), req.Limit), nil
}

// completions is what the frequency list for the prefix's script offers; nothing when
// the prefix is not one word, no list is installed, or the engine has not started.
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
