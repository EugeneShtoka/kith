package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// RoomUpgrade is a room's place in an upgrade chain; either link is usually empty.
type RoomUpgrade struct {
	Replacement domain.RoomID
	Predecessor domain.RoomID
}

// RoomUpgrade returns one room's upgrade links; known is false when no row exists
// (nearly every room).
func (c *Cache) RoomUpgrade(ctx context.Context, roomID domain.RoomID) (up RoomUpgrade, known bool, err error) {
	var replacement, predecessor string
	row := c.db.QueryRowContext(ctx,
		"SELECT replacement, predecessor FROM room_upgrades WHERE room_id = ?", string(roomID))
	known, err = optional(row.Scan(&replacement, &predecessor))
	if !known {
		if err != nil {
			err = fmt.Errorf("db: read room upgrade %s: %w", roomID, err)
		}
		return RoomUpgrade{}, false, err
	}
	return RoomUpgrade{
		Replacement: domain.RoomID(replacement),
		Predecessor: domain.RoomID(predecessor),
	}, true, nil
}

// SaveRoomUpgrade records a room's upgrade links. An empty link means "this call
// does not know", so each end is written by whichever call discovered it and the
// CASEs make the writes commute.
func (c *Cache) SaveRoomUpgrade(ctx context.Context, roomID domain.RoomID, up RoomUpgrade) error {
	return c.execInRoom(ctx, roomID, "save room upgrade "+string(roomID), `
		INSERT INTO room_upgrades(room_id, replacement, predecessor, checked_ms) VALUES(?, ?, ?, ?)
		ON CONFLICT(room_id) DO UPDATE SET
			replacement = CASE WHEN excluded.replacement <> '' THEN excluded.replacement ELSE replacement END,
			predecessor = CASE WHEN excluded.predecessor <> '' THEN excluded.predecessor ELSE predecessor END,
			checked_ms  = excluded.checked_ms`,
		string(roomID), string(up.Replacement), string(up.Predecessor), time.Now().UnixMilli())
}

// chainLimit bounds a chain walk, so a cycle in the cache cannot loop forever.
const chainLimit = 16

// RoomChains expands rooms to every room they descend from, keeping order and
// dropping duplicates. It reads the (small) link table once and walks in memory;
// it runs per keystroke from word completion. Empty stays empty.
func (c *Cache) RoomChains(ctx context.Context, roomIDs []domain.RoomID) ([]domain.RoomID, error) {
	if len(roomIDs) == 0 {
		return roomIDs, nil
	}
	links, err := c.predecessors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RoomID, 0, len(roomIDs))
	seen := make(map[domain.RoomID]bool, len(roomIDs))
	for _, roomID := range roomIDs {
		link, onChain := roomID, map[domain.RoomID]bool{roomID: true}
		for hop := 0; ; hop++ {
			if !seen[link] {
				seen[link] = true
				out = append(out, link)
			}
			if hop >= chainLimit {
				break
			}
			prev, ok := links[link]
			if !ok || prev == "" || onChain[prev] {
				break
			}
			onChain[prev] = true
			link = prev
		}
	}
	return out, nil
}

// predecessors is every recorded predecessor link, by room.
func (c *Cache) predecessors(ctx context.Context) (map[domain.RoomID]domain.RoomID, error) {
	return collectMap(ctx, c.db, "room upgrades",
		`SELECT room_id, predecessor FROM room_upgrades WHERE predecessor <> ''`,
		func(rows *sql.Rows) (domain.RoomID, domain.RoomID, error) {
			var room, predecessor string
			if err := rows.Scan(&room, &predecessor); err != nil {
				return "", "", err
			}
			return domain.RoomID(room), domain.RoomID(predecessor), nil
		})
}
