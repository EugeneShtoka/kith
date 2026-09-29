package matrix

import (
	"context"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/event"
)

// noteRefusedReaction learns what a bridged network will not accept: a reaction of
// ours redacted by someone else who is a bridge. All three conditions matter (own
// un-reacts and moderators in native rooms must not count).
func (b *InProc) noteRefusedReaction(ctx context.Context, r domain.Reaction, redaction *event.Event) {
	me := string(b.client.UserID)
	if r.Sender != me || string(redaction.Sender) == me {
		return
	}
	protocol := domain.ProtocolOf(string(redaction.Sender))
	if !protocol.IsBridged() {
		return
	}
	b.warnIf(ctx, b.cache.SaveReactionRefusal(ctx, protocol.String(), r.Key, time.Now()), "cache reaction refusal", "protocol", protocol.String())
}

// EmojiScores weighs each emoji's usage for a room, over the rooms the scope admits.
func (b *InProc) EmojiScores(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, scope string) (map[string]int, error) {
	return fromCache(b, "emoji scores", func(c *db.Cache) (map[string]int, error) {
		return c.EmojiScores(ctx, kind, roomID, spaceRooms, string(b.client.UserID), scope)
	})
}

// ReactionRefusals is what has been learned about what bridges will not deliver.
func (b *InProc) ReactionRefusals(ctx context.Context) ([]domain.ReactionRefusal, error) {
	return fromCache(b, "reaction refusals", func(c *db.Cache) ([]domain.ReactionRefusal, error) {
		return c.ReactionRefusals(ctx)
	})
}

// RecordReactionRefusal notes a refusal the client observed (its own send failing).
func (b *InProc) RecordReactionRefusal(ctx context.Context, protocol, emoji string) error {
	return toCache(b, "record reaction refusal", func(c *db.Cache) error {
		return c.SaveReactionRefusal(ctx, protocol, emoji, time.Now())
	})
}

// onReaction caches a live m.reaction and streams the add.
func (b *InProc) onReaction(ctx context.Context, evt *event.Event) {
	r, ok := toReaction(evt)
	if !ok {
		return
	}
	if b.cache != nil {
		b.warnIf(ctx, b.cache.SaveReactions(ctx, []domain.Reaction{r}), "cache reaction", "room", evt.RoomID, "event", evt.ID)
	}
	emit(&b.out, b.out.reactions, domain.ReactionUpdate{Reaction: r})
}

// RecordEmoji notes an emoji the user composed (reactions record themselves from sync).
func (b *InProc) RecordEmoji(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string) error {
	return toCache(b, "record "+string(kind)+" emoji", func(c *db.Cache) error {
		return c.RecordEmoji(ctx, kind, roomID, emoji, time.Now().UnixMilli())
	})
}

// CachedReactions returns a room's cached reactions (nil without a cache).
func (b *InProc) CachedReactions(ctx context.Context, roomID domain.RoomID) ([]domain.Reaction, error) {
	return fromCache(b, "read cached reactions", func(c *db.Cache) ([]domain.Reaction, error) { return c.Reactions(ctx, roomID) })
}
