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

// SaveInvites reconciles owner's cached invitations with the complete set /sync
// delivers; another account's are untouched. Unlike SaveRooms an empty set clears them:
// invite rows carry no history.
func (c *Cache) SaveInvites(ctx context.Context, owner domain.RoomOwner, invites []domain.Room) error {
	for i := range invites {
		if !owner.Owns(invites[i].ID) {
			return fmt.Errorf("db: %s is not among %q's rooms", invites[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertRooms(ctx, tx, invites, membershipInvite); err != nil {
			return err
		}
		if len(invites) == 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM rooms WHERE membership = ? AND substr(id, 1, ?) = ?",
				membershipInvite, len(owner), string(owner)); err != nil {
				return fmt.Errorf("db: clear %q's invites: %w", owner, err)
			}
			return nil
		}
		return sweepOwnedRooms(ctx, tx, owner, membershipInvite, invites)
	})
}
