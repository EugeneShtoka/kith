package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetStarred replaces a room's starred set with ids (account data carries the
// whole set). Existing stars keep their timestamp; new ones get atMillis.
func (c *Cache) SetStarred(ctx context.Context, roomID domain.RoomID, ids []domain.EventID, atMillis int64) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if len(ids) == 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM starred WHERE room_id = ?`, string(roomID))
			if err != nil {
				return fmt.Errorf("db: clear stars for %s: %w", roomID, err)
			}
			return nil
		}
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		in, keep := inIDs([]any{string(roomID)}, ids)
		// #nosec G202 -- inIDs emits only placeholders; every event ID is bound.
		prune := `DELETE FROM starred WHERE room_id = ? AND event_id NOT` + in
		_, err := tx.ExecContext(ctx, prune, keep...)
		if err != nil {
			return fmt.Errorf("db: prune stars for %s: %w", roomID, err)
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO starred(room_id, event_id, at_ms) VALUES(?, ?, ?)
			 ON CONFLICT(room_id, event_id) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("db: prepare star insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, id := range ids {
			if _, err := stmt.ExecContext(ctx, string(roomID), string(id), atMillis); err != nil {
				return fmt.Errorf("db: star %s in %s: %w", id, roomID, err)
			}
		}
		return nil
	})
}

// Starred is a room's starred event IDs, newest star first.
func (c *Cache) Starred(ctx context.Context, roomID domain.RoomID) ([]domain.EventID, error) {
	return collect(ctx, c.db, "starred",
		`SELECT event_id FROM starred WHERE room_id = ? ORDER BY at_ms DESC, event_id`,
		func(rows *sql.Rows) (domain.EventID, error) {
			var id string
			if err := rows.Scan(&id); err != nil {
				return "", err
			}
			return domain.EventID(id), nil
		}, string(roomID))
}
