package matrix

import (
	"context"
	"time"

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
