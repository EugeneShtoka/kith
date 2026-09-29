package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Row-loop helpers: every query here checks rows.Err through these, so an
// interrupted iteration cannot return a short list silently. `what` names the
// query in errors.

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// collect runs a query and scans every row; an empty result is not an error.
func collect[T any](ctx context.Context, db querier, what, query string,
	scan func(*sql.Rows) (T, error), args ...any,
) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query %s: %w", what, err)
	}
	return collectRows(rows, what, scan)
}

// collectRows is collect's loop for rows the caller already has. It closes them.
func collectRows[T any](rows *sql.Rows, what string, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()

	var out []T
	for rows.Next() {
		v, serr := scan(rows)
		if serr != nil {
			return nil, fmt.Errorf("db: scan %s: %w", what, serr)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate %s: %w", what, err)
	}
	return out, nil
}

// collectMap is collect for a lookup; the map is never nil.
func collectMap[K comparable, V any](ctx context.Context, db querier, what, query string,
	scan func(*sql.Rows) (K, V, error), args ...any,
) (map[K]V, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[K]V)
	for rows.Next() {
		k, v, serr := scan(rows)
		if serr != nil {
			return nil, fmt.Errorf("db: scan %s: %w", what, serr)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate %s: %w", what, err)
	}
	return out, nil
}

// sqliteMaxParams is SQLITE_MAX_VARIABLE_NUMBER for this driver (probed).
const sqliteMaxParams = 32766

// optional maps sql.ErrNoRows from a single-row Scan to (false, nil).
func optional(err error) (bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// jsonListAt is how long an ID list gets a placeholder per ID; a longer one is bound
// as one JSON array, read by json_each. Far under sqliteMaxParams, so the statement's
// other values always fit, and above what the hot paths bind (a space's rooms), so
// their plans are the placeholder ones.
const jsonListAt = 1000

// inIDs is " IN (...)" for ids, with args extended to bind them in that place.
func inIDs[T ~string](args []any, ids []T) (string, []any) {
	if len(ids) <= jsonListAt {
		return " IN (" + placeholders(len(ids)) + ")", bindIDs(args, ids)
	}
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = string(id)
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		// A []string always encodes; were it not to, the placeholders' own error (too
		// many variables) is the one the caller reports.
		return " IN (" + placeholders(len(ids)) + ")", bindIDs(args, ids)
	}
	return " IN (SELECT value FROM json_each(?))", append(args, string(encoded))
}

// bindIDs appends ids to args as bound string parameters.
func bindIDs[T ~string](args []any, ids []T) []any {
	for _, id := range ids {
		args = append(args, string(id))
	}
	return args
}

// execInRoom registers roomID and runs one statement in the same transaction, so a
// failure cannot leave an orphan placeholder row.
func (c *Cache) execInRoom(ctx context.Context, roomID domain.RoomID, what, query string, args ...any) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("db: %s: %w", what, err)
		}
		return nil
	})
}
