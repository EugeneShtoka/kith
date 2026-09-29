package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Queries only the tests ask; production reads these through the queries beside them.

// RoomChain returns roomID and every cached room it descends from, newest first.
// Backward only: predecessors are the same conversation before the upgrade.
func (c *Cache) RoomChain(ctx context.Context, roomID domain.RoomID) ([]domain.RoomID, error) {
	return c.RoomChains(ctx, []domain.RoomID{roomID})
}

// RoomParents maps every resolved room to its canonical parent space; "" means
// asked and has none.
func (c *Cache) RoomParents(ctx context.Context) (map[domain.RoomID]domain.SpaceID, error) {
	return collectMap(ctx, c.db, "room parents",
		"SELECT room_id, space_id FROM room_parents",
		func(rows *sql.Rows) (domain.RoomID, domain.SpaceID, error) {
			var roomID, spaceID string
			err := rows.Scan(&roomID, &spaceID)
			return domain.RoomID(roomID), domain.SpaceID(spaceID), err
		})
}

// IsStarred reports whether one message carries a star.
func (c *Cache) IsStarred(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (bool, error) {
	var one int
	err := c.db.QueryRowContext(ctx,
		`SELECT 1 FROM starred WHERE room_id = ? AND event_id = ?`,
		string(roomID), string(eventID)).Scan(&one)
	found, err := optional(err)
	if err != nil {
		return false, fmt.Errorf("db: read star for %s: %w", eventID, err)
	}
	return found, nil
}
