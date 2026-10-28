package db

import (
	"context"
	"database/sql"
	"fmt"
)

// numberTouched gives each message tx touched (see messageChanges) its next number,
// in the order first touched. A message deleted since it was touched gets none.
func numberTouched(ctx context.Context, tx *sql.Tx) error {
	var touched bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM message_touched)").Scan(&touched); err != nil {
		return fmt.Errorf("db: read what changed: %w", err)
	}
	if !touched {
		return nil
	}
	// WHERE true: an upsert's SELECT would otherwise read ON CONFLICT as a join's ON.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO message_changes(room_id, event_id, change)
		SELECT t.room_id, t.event_id, (SELECT last FROM change_counter) + row_number() OVER (ORDER BY min(t.rowid))
		  FROM message_touched t
		  JOIN messages m ON m.room_id = t.room_id AND m.event_id = t.event_id
		 WHERE true
		 GROUP BY t.room_id, t.event_id
		ON CONFLICT(room_id, event_id) DO UPDATE SET change = excluded.change`)
	if err != nil {
		return fmt.Errorf("db: number what changed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: count what changed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE change_counter SET last = last + ?", n); err != nil {
		return fmt.Errorf("db: count the numbers given: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM message_touched"); err != nil {
		return fmt.Errorf("db: forget what was numbered: %w", err)
	}
	return nil
}
