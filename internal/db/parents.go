package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// RoomParent returns one room's cached canonical parent; known is false when never
// asked.
func (c *Cache) RoomParent(ctx context.Context, roomID domain.RoomID) (parent domain.SpaceID, known bool, err error) {
	var spaceID string
	row := c.db.QueryRowContext(ctx, "SELECT space_id FROM room_parents WHERE room_id = ?", string(roomID))
	known, err = optional(row.Scan(&spaceID))
	if err != nil {
		return "", false, fmt.Errorf("db: read room parent %s: %w", roomID, err)
	}
	return domain.SpaceID(spaceID), known, nil
}

// SaveRoomParent records a room's canonical parent, or its absence (so it is not
// re-asked).
func (c *Cache) SaveRoomParent(ctx context.Context, roomID domain.RoomID, spaceID domain.SpaceID) error {
	return c.execInRoom(ctx, roomID, "save room parent "+string(roomID), `
		INSERT INTO room_parents(room_id, space_id, resolved_ms) VALUES(?, ?, ?)
		ON CONFLICT(room_id) DO UPDATE SET space_id=excluded.space_id, resolved_ms=excluded.resolved_ms`,
		string(roomID), string(spaceID), time.Now().UnixMilli())
}

// OriginSpaces is every space some room names as its canonical parent.
func (c *Cache) OriginSpaces(ctx context.Context) (map[domain.SpaceID]bool, error) {
	return collectMap(ctx, c.db, "origin spaces",
		"SELECT DISTINCT space_id FROM room_parents WHERE space_id != ''",
		func(rows *sql.Rows) (domain.SpaceID, bool, error) {
			var spaceID string
			err := rows.Scan(&spaceID)
			return domain.SpaceID(spaceID), true, err
		})
}
