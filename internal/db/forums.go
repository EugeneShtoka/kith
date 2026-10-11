package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ForgetThreads drops a room's thread messages, their roots and read positions, for
// a network that now keeps threads as rooms of their own (a forum's topics).
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

// ForgetOwned drops every room and space of owner, with everything kept of them.
func (c *Cache) ForgetOwned(ctx context.Context, owner domain.RoomOwner) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"rooms", "spaces"} {
			// #nosec G202 -- table is one of two fixed names; the owner is bound.
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE substr(id, 1, ?) = ?", len(owner), string(owner)); err != nil {
				return fmt.Errorf("db: forget %q's %s: %w", owner, table, err)
			}
		}
		return nil
	})
}

// ForgetRooms drops rooms, with everything kept of them; a space ID drops the space.
func (c *Cache) ForgetRooms(ctx context.Context, rooms []domain.RoomID) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for _, room := range rooms {
			if _, err := tx.ExecContext(ctx, "DELETE FROM rooms WHERE id = ?", string(room)); err != nil {
				return fmt.Errorf("db: forget %s: %w", room, err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM spaces WHERE id = ?", string(room)); err != nil {
				return fmt.Errorf("db: forget the space %s: %w", room, err)
			}
		}
		return nil
	})
}
