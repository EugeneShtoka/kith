package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetSpam records a room's spam verdict or release (mirroring account data),
// replacing the other. The room row comes first: account data can arrive before
// the room list, and a dropped verdict would never be re-caught.
func (c *Cache) SetSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, verdict.Room); err != nil {
			return err
		}
		if err := clearSpamIn(ctx, tx, verdict.Room); err != nil {
			return err
		}
		if verdict.Released {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO spam_released (room_id, at_ms) VALUES (?, ?)`,
				string(verdict.Room), verdict.At.UnixMilli())
			if err != nil {
				return fmt.Errorf("db: record spam release for %s: %w", verdict.Room, err)
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO spam_rooms (room_id, rule, filter, at_ms) VALUES (?, ?, ?, ?)`,
			string(verdict.Room), int(verdict.Rule), verdict.Filter, verdict.At.UnixMilli())
		if err != nil {
			return fmt.Errorf("db: record spam for %s: %w", verdict.Room, err)
		}
		return nil
	})
}

// ClearSpam forgets both a verdict and a release.
func (c *Cache) ClearSpam(ctx context.Context, roomID domain.RoomID) error {
	return c.inTx(ctx, func(tx *sql.Tx) error { return clearSpamIn(ctx, tx, roomID) })
}

func clearSpamIn(ctx context.Context, tx *sql.Tx, roomID domain.RoomID) error {
	for _, stmt := range []string{
		`DELETE FROM spam_rooms WHERE room_id = ?`,
		`DELETE FROM spam_released WHERE room_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, string(roomID)); err != nil {
			return fmt.Errorf("db: clear spam for %s: %w", roomID, err)
		}
	}
	return nil
}

// SpamRooms is every caught room plus every released one (Released set), newest
// first. Callers wanting only Spam check each verdict's Spam().
func (c *Cache) SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error) {
	return collect(ctx, c.db, "spam_rooms",
		`SELECT room_id, rule, filter, at_ms, 0 FROM spam_rooms
		 UNION ALL
		 SELECT room_id, 0, '', at_ms, 1 FROM spam_released
		 ORDER BY 4 DESC, 1`,
		func(rows *sql.Rows) (domain.SpamVerdict, error) {
			var room, filter string
			var rule, released int
			var at int64
			if err := rows.Scan(&room, &rule, &filter, &at, &released); err != nil {
				return domain.SpamVerdict{}, err
			}
			return domain.SpamVerdict{
				Room:     domain.RoomID(room),
				Rule:     domain.SpamRule(rule),
				Filter:   filter,
				At:       time.UnixMilli(at),
				Released: released != 0,
			}, nil
		})
}
