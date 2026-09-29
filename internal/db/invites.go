package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Invites returns the cached pending invitations, sorted for display.
func (c *Cache) Invites(ctx context.Context) ([]domain.Room, error) {
	invites, err := c.roomsWith(ctx, membershipInvite)
	if err != nil {
		return nil, err
	}
	domain.SortRooms(invites)
	return invites, nil
}

// SaveInvites reconciles cached invitations with the complete set /sync delivers.
// Unlike SaveRooms an empty set clears them: invite rows carry no history.
func (c *Cache) SaveInvites(ctx context.Context, invites []domain.Room) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertRooms(ctx, tx, invites, membershipInvite); err != nil {
			return err
		}
		if len(invites) == 0 {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM rooms WHERE membership = ?", membershipInvite); err != nil {
				return fmt.Errorf("db: clear invites: %w", err)
			}
			return nil
		}
		return sweepRooms(ctx, tx, invites, membershipInvite)
	})
}
