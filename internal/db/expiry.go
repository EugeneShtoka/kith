package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A disappearing message is deleted, as a deletion is (kept under [display.deleted] keep),
// once its timer has run from when its countdown started: when it was sent, or (Signal,
// for what others send) when the account read it. message_expiry holds its timer and
// countdown until then.

// SetExpiry gives a cached message its timer, its countdown started at starts (zero: when
// the account reads it, which may be already). A countdown started is never restarted.
func (c *Cache) SetExpiry(ctx context.Context, room domain.RoomID, event domain.EventID, timer time.Duration, starts time.Time) error {
	if timer <= 0 {
		return nil
	}
	var startsMS int64
	if !starts.IsZero() {
		startsMS = starts.UnixMilli()
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_expiry(room_id, event_id, seconds, starts_ms)
			SELECT ?1, ?2, ?3, ?4 WHERE EXISTS
				(SELECT 1 FROM messages WHERE room_id = ?1 AND event_id = ?2 AND redacted = 0)
			ON CONFLICT(room_id, event_id) DO UPDATE SET
				seconds = excluded.seconds,
				starts_ms = CASE WHEN message_expiry.starts_ms > 0 THEN message_expiry.starts_ms ELSE excluded.starts_ms END`,
			string(room), string(event), int64(timer/time.Second), startsMS); err != nil {
			return fmt.Errorf("db: keep the timer of %s: %w", event, err)
		}
		return startCountdowns(ctx, tx, room, time.Now())
	})
}

// startCountdowns starts, at now, the countdown of each disappearing message in room the
// account has read and whose countdown waits for that.
func startCountdowns(ctx context.Context, tx *sql.Tx, room domain.RoomID, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE message_expiry SET starts_ms = ?2
		WHERE room_id = ?1 AND starts_ms = 0 AND event_id IN (
			SELECT m.event_id FROM messages m
			WHERE m.room_id = ?1 AND m.ts_ms <= (SELECT read_ts_ms FROM room_unread WHERE room_id = ?1))`,
		string(room), now.UnixMilli()); err != nil {
		return fmt.Errorf("db: start the countdowns read in %s: %w", room, err)
	}
	return nil
}

// ExpireDue deletes owner's disappearing messages whose timers have run by now, as a
// deletion does (keep keeps what they said), and returns them as they were, marked
// deleted, for clients to hear of.
func (c *Cache) ExpireDue(ctx context.Context, owner domain.RoomOwner, now time.Time, keep bool) ([]domain.Message, error) {
	type due struct {
		room  domain.RoomID
		event domain.EventID
		at    int64
	}
	dues, err := collect(ctx, c.db, "what has expired", `
		SELECT room_id, event_id, starts_ms + seconds * 1000 FROM message_expiry
		WHERE starts_ms > 0 AND starts_ms + seconds * 1000 <= ? AND substr(room_id, 1, ?) = ?`,
		func(rows *sql.Rows) (due, error) {
			var d due
			var room, event string
			err := rows.Scan(&room, &event, &d.at)
			d.room, d.event = domain.RoomID(room), domain.EventID(event)
			return d, err
		}, now.UnixMilli(), len(owner), string(owner))
	if err != nil {
		return nil, err
	}
	var gone []domain.Message
	for _, d := range dues {
		msg, ok, err := c.MessageByID(ctx, d.room, d.event)
		if err != nil {
			return gone, err
		}
		at := time.UnixMilli(d.at)
		if err := c.MarkRedacted(ctx, d.room, d.event, "", "", at, keep); err != nil {
			return gone, err
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM message_expiry WHERE room_id = ? AND event_id = ?`,
			string(d.room), string(d.event)); err != nil {
			return gone, fmt.Errorf("db: let the timer of %s go: %w", d.event, err)
		}
		if ok {
			msg.Redacted, msg.RedactedAt = true, at
			gone = append(gone, msg)
		}
	}
	return gone, nil
}
