package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room's network never changes: a room a bridge keeps is that network's for good.
// So it is read once and kept, never rewritten; a room read and found to be no
// bridge's keeps ProtocolMatrix, so what is known is exactly what has a row.

// RoomsOfUnknownNetwork is owner's joined rooms whose network has not been read yet.
func (c *Cache) RoomsOfUnknownNetwork(ctx context.Context, owner domain.RoomOwner) ([]domain.RoomID, error) {
	return collect(ctx, c.db, "rooms of unknown network",
		`SELECT r.id FROM rooms r
		  WHERE r.membership = 'join' AND substr(r.id, 1, ?) = ?
		    AND NOT EXISTS (SELECT 1 FROM room_networks n WHERE n.room_id = r.id)`,
		func(rows *sql.Rows) (domain.RoomID, error) {
			var id string
			err := rows.Scan(&id)
			return domain.RoomID(id), err
		}, len(owner), string(owner))
}

// KeepRoomNetworks keeps the network each room was read to be on, for a room not
// read before; one already known keeps what it had. A room the cache does not hold
// yet is registered (registerRoom), as an archived one is.
func (c *Cache) KeepRoomNetworks(ctx context.Context, networks map[domain.RoomID]domain.Protocol) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for room, network := range networks {
			if network == "" {
				continue
			}
			if err := registerRoom(ctx, tx, room); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO room_networks(room_id, network) VALUES(?, ?) ON CONFLICT(room_id) DO NOTHING",
				string(room), string(network)); err != nil {
				return fmt.Errorf("db: keep %s's network: %w", room, err)
			}
		}
		return nil
	})
}
