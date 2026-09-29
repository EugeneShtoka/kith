package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sender color slots: a position on the hue wheel per person per room, not a color,
// so a theme switch recolors consistently. Persisted because assignment is ordinal.

// SenderSlots is a room's remembered slots, keyed by identity group.
func (c *Cache) SenderSlots(ctx context.Context, roomID domain.RoomID) (map[string]int, error) {
	return collectMap(ctx, c.db, "sender slots for "+string(roomID),
		"SELECT group_key, slot FROM sender_slots WHERE room_id = ?",
		func(rows *sql.Rows) (string, int, error) {
			var key string
			var slot int
			err := rows.Scan(&key, &slot)
			return key, slot, err
		}, string(roomID))
}

// SaveSenderSlots records new slots for a room. A stored slot is never changed:
// a hue once given is kept, even against a client with a colder cache.
func (c *Cache) SaveSenderSlots(ctx context.Context, roomID domain.RoomID, slots map[string]int) error {
	if roomID == "" || len(slots) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO sender_slots(room_id, group_key, slot) VALUES(?, ?, ?)
			ON CONFLICT(room_id, group_key) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("db: prepare sender slots: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for key, slot := range slots {
			if key == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(roomID), key, slot); err != nil {
				return fmt.Errorf("db: save sender slot %s/%s: %w", roomID, key, err)
			}
		}
		return nil
	})
}
