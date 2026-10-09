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

// ForgetOwned drops every room and space owner has from the cache, with everything
// kept of them: an account signed out whose history is not to stay.
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

// ForgetRooms drops rooms from the cache, with everything kept of them: rooms left. A
// space is a room too (a Matrix space, a WhatsApp community), and goes as one.
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
