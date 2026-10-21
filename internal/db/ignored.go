package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetMatrixIgnored replaces the ignored MXIDs for network with ids. The caller
// hands over the whole set (as the Matrix account-data event does), so this
// method deletes the rows for ids no longer there and inserts the new ones.
func (c *Cache) SetMatrixIgnored(ctx context.Context, network domain.Protocol, ids []string) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if len(ids) == 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM ignored_users WHERE network = ?`, network.String())
			if err != nil {
				return fmt.Errorf("db: clear ignored list for %s: %w", network, err)
			}
			return nil
		}
		in, keep := inIDs([]any{network.String()}, ids)
		// #nosec G202 -- inIDs emits only placeholders; every id is bound.
		prune := `DELETE FROM ignored_users WHERE network = ? AND user_id NOT` + in
		if _, err := tx.ExecContext(ctx, prune, keep...); err != nil {
			return fmt.Errorf("db: prune ignored list for %s: %w", network, err)
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO ignored_users(network, user_id) VALUES(?, ?)
			 ON CONFLICT(network, user_id) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("db: prepare ignored insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, id := range ids {
			if _, err := stmt.ExecContext(ctx, network.String(), id); err != nil {
				return fmt.Errorf("db: ignore %s in %s: %w", id, network, err)
			}
		}
		return nil
	})
}

// matrixIgnored is the MXIDs this account ignores on network, alphabetically.
func (c *Cache) matrixIgnored(ctx context.Context, network domain.Protocol) ([]string, error) {
	return collect(ctx, c.db, "ignored_users",
		`SELECT user_id FROM ignored_users WHERE network = ? ORDER BY user_id`,
		func(rows *sql.Rows) (string, error) {
			var id string
			if err := rows.Scan(&id); err != nil {
				return "", err
			}
			return id, nil
		}, network.String())
}
