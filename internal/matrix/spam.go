package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// spamType is the room account data type holding a room's spam verdict. Account data
// rather than cache: rules fire once, so a verdict lost on a cache rebuild would never
// be caught again. Invisible to the room.
const spamType = "org.kith.spam"

// spamContent is one room's verdict (latest rule wins), or a release, written with
// rule 0.
type spamContent struct {
	Rule     int    `json:"rule"`
	Filter   string `json:"filter,omitempty"`
	At       int64  `json:"at_ms"`
	Released bool   `json:"released,omitempty"`
}

// verdict is the content as the domain reads it; a release outranks a rule.
func (c spamContent) verdict(roomID domain.RoomID) domain.SpamVerdict {
	if c.Released {
		return domain.SpamVerdict{Room: roomID, Released: true, At: time.UnixMilli(c.At)}
	}
	return domain.SpamVerdict{
		Room: roomID, Rule: domain.SpamRule(c.Rule), Filter: c.Filter, At: time.UnixMilli(c.At),
	}
}

// spamFrom reports one room's spam account data in a sync, and whether the type was
// present at all (absence is silence, not a release).
func spamFrom(events []*event.Event) (content spamContent, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != spamType {
			continue
		}
		var parsed spamContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &parsed); err != nil {
			// Unparseable still means present; empty reads as "not spam" (the safe side).
			return spamContent{}, true
		}
		content, present = parsed, true
	}
	return content, present
}

// MarkSpam records a verdict, a release, or (neither) clears it. The homeserver is
// written first, then the mirror. A rule's verdict never overwrites a release: the
// server's copy is checked first (domain.ErrSpamReleased), since the release may come
// from another machine and not have synced yet.
func (b *InProc) MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	at := verdict.At
	if at.IsZero() {
		at = time.Now()
	}
	verdict.At = at
	var content spamContent
	switch {
	case verdict.Released:
		content = spamContent{Released: true, At: at.UnixMilli()}
	case !verdict.Spam():
		return b.clearSpam(ctx, verdict.Room)
	default:
		if verdict.Rule != domain.SpamByHand {
			if err := b.refuseOverRelease(ctx, verdict.Room); err != nil {
				return err
			}
		}
		content = spamContent{Rule: int(verdict.Rule), Filter: verdict.Filter, At: at.UnixMilli()}
	}
	if err := b.client.SetRoomAccountData(ctx, id.RoomID(verdict.Room), spamType, &content); err != nil {
		return fmt.Errorf("matrix: record spam for %s: %w", verdict.Room, err)
	}
	return toCache(b, "mirror spam for "+string(verdict.Room), func(c *db.Cache) error {
		return c.SetSpam(ctx, content.verdict(verdict.Room))
	})
}

// refuseOverRelease returns domain.ErrSpamReleased for a room released in the mirror
// or on the homeserver. An unreadable server copy is an error, not a go-ahead.
func (b *InProc) refuseOverRelease(ctx context.Context, roomID domain.RoomID) error {
	if b.cache != nil {
		if verdicts, err := b.cache.SpamRooms(ctx); err == nil {
			for i := range verdicts {
				if verdicts[i].Room == roomID && verdicts[i].Released {
					return fmt.Errorf("matrix: record spam for %s: %w", roomID, domain.ErrSpamReleased)
				}
			}
		}
	}
	var current spamContent
	err := b.client.GetRoomAccountData(ctx, id.RoomID(roomID), spamType, &current)
	switch {
	case errors.Is(err, mautrix.MNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("matrix: read spam for %s before recording: %w", roomID, err)
	case !current.Released:
		return nil
	}
	if b.cache != nil {
		b.warnIf(ctx, b.cache.SetSpam(ctx, current.verdict(roomID)), "mirror released spam verdict", "room", roomID)
	}
	return fmt.Errorf("matrix: record spam for %s: %w", roomID, domain.ErrSpamReleased)
}

// clearSpam writes empty content (neither verdict nor release) and clears the mirror.
func (b *InProc) clearSpam(ctx context.Context, roomID domain.RoomID) error {
	if err := b.client.SetRoomAccountData(ctx, id.RoomID(roomID), spamType, &spamContent{}); err != nil {
		return fmt.Errorf("matrix: clear spam for %s: %w", roomID, err)
	}
	return toCache(b, "clear mirrored spam for "+string(roomID), func(c *db.Cache) error {
		return c.ClearSpam(ctx, roomID)
	})
}

// SpamRooms is every caught room from the cache (mirrored from account data).
func (b *InProc) SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error) {
	return fromCache(b, "list spam rooms", func(c *db.Cache) ([]domain.SpamVerdict, error) {
		return c.SpamRooms(ctx)
	})
}

// spamMirror applies a sync's verdict to the cache. Best-effort: the server copy is
// the record, and an initial sync restates it.
func (b *InProc) spamMirror(ctx context.Context, roomID domain.RoomID, content spamContent) {
	if b.cache == nil {
		return
	}
	verdict := content.verdict(roomID)
	if !verdict.Spam() && !verdict.Released {
		b.warnIf(ctx, b.cache.ClearSpam(ctx, roomID), "clear mirrored spam verdict", "room", roomID)
		return
	}
	b.warnIf(ctx, b.cache.SetSpam(ctx, verdict), "mirror spam verdict", "room", roomID)
}
