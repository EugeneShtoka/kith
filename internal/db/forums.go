package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ForgetThreads drops a room's threads from the cache: every message in one, the
// messages that began them (roots), and how far each was read. A network that now
// keeps what were threads as rooms of their own (a forum's topics) refetches them there.
func (c *Cache) ForgetThreads(ctx context.Context, room domain.RoomID, roots []domain.EventID) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE room_id = ? AND thread_root <> ''", string(room)); err != nil {
			return fmt.Errorf("db: forget the threads of %s: %w", room, err)
		}
		for _, root := range roots {
			if _, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE room_id = ? AND event_id = ?", string(room), string(root)); err != nil {
				return fmt.Errorf("db: forget the start of a thread of %s: %w", room, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM thread_read WHERE room_id = ?", string(room)); err != nil {
			return fmt.Errorf("db: forget how far the threads of %s were read: %w", room, err)
		}
		return nil
	})
}
