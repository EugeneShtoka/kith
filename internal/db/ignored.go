package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetMatrixIgnored replaces the people account ignores with ids. The caller hands over
// the whole set (as the Matrix account-data event does), so this method deletes the
// rows for ids no longer there and inserts the new ones; other accounts' are untouched.
func (c *Cache) SetMatrixIgnored(ctx context.Context, account domain.RoomOwner, ids []string) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if len(ids) == 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM ignored_users WHERE account = ?`, string(account))
			if err != nil {
				return fmt.Errorf("db: clear the ignored list of %s: %w", account, err)
			}
			return nil
		}
		in, keep := inIDs([]any{string(account)}, ids)
		// #nosec G202 -- inIDs emits only placeholders; every id is bound.
		prune := `DELETE FROM ignored_users WHERE account = ? AND user_id NOT` + in
		if _, err := tx.ExecContext(ctx, prune, keep...); err != nil {
			return fmt.Errorf("db: prune the ignored list of %s: %w", account, err)
		}
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO ignored_users(account, user_id) VALUES(?, ?)
			 ON CONFLICT(account, user_id) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("db: prepare ignored insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, id := range ids {
			if _, err := stmt.ExecContext(ctx, string(account), id); err != nil {
				return fmt.Errorf("db: ignore %s on %s: %w", id, account, err)
			}
		}
		return nil
	})
}

// MatrixIgnored is the people any Matrix account ignores, alphabetically: ignoring
// someone applies to every account.
func (c *Cache) MatrixIgnored(ctx context.Context) ([]string, error) {
	return collect(ctx, c.db, "ignored_users",
		`SELECT DISTINCT user_id FROM ignored_users ORDER BY user_id`,
		func(rows *sql.Rows) (string, error) {
			var id string
			if err := rows.Scan(&id); err != nil {
				return "", err
			}
			return id, nil
		})
}
