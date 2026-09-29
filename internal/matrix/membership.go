package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Membership moderation. Power levels are checked here first so a refusal can name
// the level needed and held (ErrNoPower) instead of surfacing M_FORBIDDEN. The check
// is advisory; the homeserver still decides.

// canAct checks our power against need and, when target is set, that we strictly
// outrank the target. Unreadable power levels are not a refusal: the server decides.
func (b *InProc) canAct(ctx context.Context, roomID domain.RoomID, target string, need func(*event.PowerLevelsEventContent) int, verb string) error {
	var levels event.PowerLevelsEventContent
	if err := b.client.StateEvent(ctx, id.RoomID(roomID), event.StatePowerLevels, "", &levels); err != nil {
		return nil //nolint:nilerr // a failed read is not a refusal; see above
	}
	mine := levels.GetUserLevel(b.client.UserID)
	if required := need(&levels); mine < required {
		return fmt.Errorf("%w: you need power %d in this room to %s, and you have %d",
			api.ErrNoPower, required, verb, mine)
	}
	// Acting on somebody needs strictly greater power.
	if target != "" {
		if theirs := levels.GetUserLevel(id.UserID(target)); theirs >= mine {
			return fmt.Errorf("%w: %s has power %d in this room and you have %d — you cannot %s an equal",
				api.ErrNoPower, target, theirs, mine, verb)
		}
	}
	return nil
}

// InviteUser invites somebody to a room.
func (b *InProc) InviteUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	// Inviting depends only on our own level.
	if err := b.canAct(ctx, roomID, "", (*event.PowerLevelsEventContent).Invite, "invite"); err != nil {
		return err
	}
	if _, err := b.client.InviteUser(ctx, id.RoomID(roomID), &mautrix.ReqInviteUser{UserID: id.UserID(userID)}); err != nil {
		return fmt.Errorf("matrix: invite %s: %w", userID, err)
	}
	return nil
}

// KickUser removes somebody from a room; unlike a ban they may come back.
func (b *InProc) KickUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	if err := b.canAct(ctx, roomID, userID, (*event.PowerLevelsEventContent).Kick, "remove someone"); err != nil {
		return err
	}
	if _, err := b.client.KickUser(ctx, id.RoomID(roomID), &mautrix.ReqKickUser{UserID: id.UserID(userID), Reason: reason}); err != nil {
		return fmt.Errorf("matrix: remove %s: %w", userID, err)
	}
	return nil
}

// BanUser removes somebody and keeps them out until unbanned.
func (b *InProc) BanUser(ctx context.Context, roomID domain.RoomID, userID, reason string) error {
	if err := b.canAct(ctx, roomID, userID, (*event.PowerLevelsEventContent).Ban, "ban someone"); err != nil {
		return err
	}
	if _, err := b.client.BanUser(ctx, id.RoomID(roomID), &mautrix.ReqBanUser{UserID: id.UserID(userID), Reason: reason}); err != nil {
		return fmt.Errorf("matrix: ban %s: %w", userID, err)
	}
	return nil
}

// UnbanUser lifts a ban. It takes an MXID: banned users are not in the cached membership.
func (b *InProc) UnbanUser(ctx context.Context, roomID domain.RoomID, userID string) error {
	// The ban level decides, not the target's rank.
	if err := b.canAct(ctx, roomID, "", (*event.PowerLevelsEventContent).Ban, "unban someone"); err != nil {
		return err
	}
	if _, err := b.client.UnbanUser(ctx, id.RoomID(roomID), &mautrix.ReqUnbanUser{UserID: id.UserID(userID)}); err != nil {
		return fmt.Errorf("matrix: unban %s: %w", userID, err)
	}
	return nil
}

// Redact removes a message's content. Your own needs no power; somebody else's needs
// the redact level. reason is visible to everyone; empty sends none.
func (b *InProc) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	if err := b.canRedact(ctx, roomID, eventID); err != nil {
		return err
	}
	if _, err := b.client.RedactEvent(ctx, id.RoomID(roomID), id.EventID(eventID),
		mautrix.ReqRedact{Reason: reason}); err != nil {
		return fmt.Errorf("matrix: redact %s: %w", eventID, err)
	}
	return b.redactEdits(ctx, roomID, eventID, reason)
}

// redactEdits redacts a deleted message's edits too: each m.replace carries its own
// copy of the text. Skipped when [display.deleted] keep. Runs after the deletion,
// and its failure (ErrEditsRemain) does not undo it.
func (b *InProc) redactEdits(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, reason string) error {
	if b.keepDeleted {
		return nil
	}
	resp, err := b.client.GetRelations(ctx, id.RoomID(roomID), id.EventID(eventID), &mautrix.ReqGetRelations{
		RelationType: event.RelReplace,
		Dir:          mautrix.DirectionBackward,
	})
	if err != nil {
		return fmt.Errorf("%w: listing them failed: %w", api.ErrEditsRemain, err)
	}
	var left int
	var first error
	for _, evt := range resp.Chunk {
		if evt.Unsigned.RedactedBecause != nil {
			continue // already gone
		}
		if _, rerr := b.client.RedactEvent(ctx, evt.RoomID, evt.ID, mautrix.ReqRedact{Reason: reason}); rerr != nil {
			left++
			if first == nil {
				first = rerr
			}
			b.warnIf(ctx, rerr, "redact an edit", "room", evt.RoomID, "event", evt.ID)
		}
	}
	if left > 0 {
		return fmt.Errorf("%w: %d of %d could not be removed (first: %w)", api.ErrEditsRemain, left, len(resp.Chunk), first)
	}
	return nil
}

// canRedact skips the power check for our own message; an unknown sender is
// treated as somebody else's.
func (b *InProc) canRedact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) error {
	if b.cache != nil {
		if sender, err := b.cache.SenderOf(ctx, roomID, eventID); err == nil && sender == string(b.client.UserID) {
			return nil
		}
	}
	return b.canAct(ctx, roomID, "", (*event.PowerLevelsEventContent).Redact, "delete somebody else's message")
}
