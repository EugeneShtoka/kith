package local

import (
	"context"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ReplaceDraft stores (or, when empty, removes) a room's draft in the cache, only
// while the stored one is still over.
func (s *Service) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	return fromCache(s, "replace draft", func(c *db.Cache) (bool, error) {
		return c.ReplaceDraft(ctx, draft, over)
	})
}

// Drafts returns every stored draft.
func (s *Service) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return fromCache(s, "read drafts", func(c *db.Cache) ([]domain.StoredDraft, error) {
		return c.Drafts(ctx)
	})
}

// SearchMessages searches the cache, newest first (nil without a cache).
func (s *Service) SearchMessages(ctx context.Context, req domain.SearchRequest) ([]domain.SearchHit, error) {
	if s.cache == nil {
		return nil, nil
	}
	// Expand the scope to whole upgrade chains: the FTS index is per room ID, so an
	// upgraded room's earlier history would otherwise be silently missed.
	if !req.Rooms.All {
		rooms, err := s.cache.RoomChains(ctx, req.Rooms.IDs)
		if err != nil {
			return nil, fmt.Errorf("local: expand search scope: %w", err)
		}
		req.Rooms.IDs = rooms
	}
	hits, err := s.cache.SearchMessages(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("local: search messages: %w", err)
	}
	return hits, nil
}

// RoomsWith finds rooms all these people are in, from the cache.
func (s *Service) RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error) {
	return fromCache(s, "rooms with", func(c *db.Cache) ([]domain.Room, error) {
		return c.RoomsWith(ctx, userIDs, rooms, limit)
	})
}

// MessagesAround is one cached message with its neighbors.
func (s *Service) MessagesAround(
	ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int,
) ([]domain.Message, error) {
	return fromCache(s, "messages around", func(c *db.Cache) ([]domain.Message, error) {
		return c.MessagesAround(ctx, roomID, event, before, after)
	})
}

// CachedReactions returns a room's cached reactions (nil without a cache).
func (s *Service) CachedReactions(ctx context.Context, roomID domain.RoomID) ([]domain.Reaction, error) {
	return fromCache(s, "read cached reactions", func(c *db.Cache) ([]domain.Reaction, error) { return c.Reactions(ctx, roomID) })
}

// EmojiScores weighs each emoji's usage for a room, over the rooms the scope admits.
func (s *Service) EmojiScores(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, scope string) (map[string]int, error) {
	return fromCache(s, "emoji scores", func(c *db.Cache) (map[string]int, error) {
		return c.EmojiScores(ctx, kind, roomID, spaceRooms, s.me(), scope)
	})
}

// RecordEmoji notes an emoji the user composed (reactions record themselves from sync).
func (s *Service) RecordEmoji(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string) error {
	return toCache(s, "record "+string(kind)+" emoji", func(c *db.Cache) error {
		return c.RecordEmoji(ctx, kind, roomID, emoji, time.Now().UnixMilli())
	})
}

// ReactionRefusals is what has been learned about what bridges will not deliver.
func (s *Service) ReactionRefusals(ctx context.Context) ([]domain.ReactionRefusal, error) {
	return fromCache(s, "reaction refusals", func(c *db.Cache) ([]domain.ReactionRefusal, error) {
		return c.ReactionRefusals(ctx)
	})
}

// RecordReactionRefusal notes a refusal the client observed (its own send failing).
func (s *Service) RecordReactionRefusal(ctx context.Context, protocol, emoji string) error {
	return toCache(s, "record reaction refusal", func(c *db.Cache) error {
		return c.SaveReactionRefusal(ctx, protocol, emoji, time.Now())
	})
}

// SenderSlots is a room's remembered color slots (empty: never colored).
func (s *Service) SenderSlots(ctx context.Context, roomID domain.RoomID) (map[string]int, error) {
	return fromCache(s, "sender slots for "+string(roomID), func(c *db.Cache) (map[string]int, error) {
		return c.SenderSlots(ctx, roomID)
	})
}

// SaveSenderSlots records slots a client assigned (no-op without a cache).
func (s *Service) SaveSenderSlots(ctx context.Context, roomID domain.RoomID, slots map[string]int) error {
	return toCache(s, "save sender slots for "+string(roomID), func(c *db.Cache) error {
		return c.SaveSenderSlots(ctx, roomID, slots)
	})
}

// SearchSenders ranks who posted in a set of rooms, for the search `from:` filter.
func (s *Service) SearchSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]domain.Member, error) {
	return fromCache(s, "read search senders", func(c *db.Cache) ([]domain.Member, error) {
		return c.SearchSenders(ctx, rooms, limit)
	})
}
