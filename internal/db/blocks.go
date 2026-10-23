package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetNetworkBlocked replaces the block list for network with userIDs (the server's
// current set). Rows for IDs no longer there are dropped; new ones are inserted.
func (c *Cache) SetNetworkBlocked(ctx context.Context, network domain.Protocol, userIDs []string) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if len(userIDs) == 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM network_blocks WHERE network = ?`, network.String())
			if err != nil {
				return fmt.Errorf("db: clear block list for %s: %w", network, err)
			}
			return nil
		}
		in, keep := inIDs([]any{network.String()}, userIDs)
		// #nosec G202 -- inIDs emits only placeholders; every id is bound.
		prune := `DELETE FROM network_blocks WHERE network = ? AND user_id NOT` + in
		if _, err := tx.ExecContext(ctx, prune, keep...); err != nil {
			return fmt.Errorf("db: prune block list for %s: %w", network, err)
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO network_blocks(network, user_id) VALUES(?, ?)
			 ON CONFLICT(network, user_id) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("db: prepare block insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, id := range userIDs {
			if _, err := stmt.ExecContext(ctx, network.String(), id); err != nil {
				return fmt.Errorf("db: block %s on %s: %w", id, network, err)
			}
		}
		return nil
	})
}

// NetworkBlocked is the user IDs this account has blocked on network, alphabetically.
func (c *Cache) NetworkBlocked(ctx context.Context, network domain.Protocol) ([]string, error) {
	return collect(ctx, c.db, "network_blocks",
		`SELECT user_id FROM network_blocks WHERE network = ? ORDER BY user_id`,
		func(rows *sql.Rows) (string, error) {
			var id string
			if err := rows.Scan(&id); err != nil {
				return "", err
			}
			return id, nil
		}, network.String())
}

// AllNetworkBlocked is every (network, user_id) row in the mirror, grouped by network.
// Empty when nothing has been blocked anywhere.
func (c *Cache) AllNetworkBlocked(ctx context.Context) (map[domain.Protocol][]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT network, user_id FROM network_blocks ORDER BY network, user_id`)
	if err != nil {
		return nil, fmt.Errorf("db: list all blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[domain.Protocol][]string{}
	for rows.Next() {
		var network, userID string
		if err := rows.Scan(&network, &userID); err != nil {
			return nil, fmt.Errorf("db: scan block row: %w", err)
		}
		p := domain.Protocol(network)
		out[p] = append(out[p], userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate blocks: %w", err)
	}
	return out, nil
}
