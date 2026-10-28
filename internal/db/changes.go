package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Messages are numbered as they change (see messageChanges): a reader holding the
// epoch and the last number it read asks for what changed after it, and is told when
// more has.

// OnChanged has fn called after each commit that numbered a change to a message, and
// after Clear. It runs on the writer's goroutine, after the commit, so it must return
// at once. Call before use.
func (c *Cache) OnChanged(fn func()) { c.changed = fn }

// LastChange is the epoch naming the cache's numbering and the last number given: a
// reader starting now reads on from it.
func (c *Cache) LastChange(ctx context.Context) (epoch string, last int64, err error) {
	if err = c.db.QueryRowContext(ctx, "SELECT epoch, last FROM change_counter").Scan(&epoch, &last); err != nil {
		return "", 0, fmt.Errorf("db: read the last change: %w", err)
	}
	return epoch, last, nil
}

// ChangesAfter is up to limit messages whose latest change is numbered after after,
// in number order, as the cache holds them now, with the epoch they are numbered in. A
// message changed again while this reads may come with its newer state under the older
// number; it comes again under the newer one.
func (c *Cache) ChangesAfter(ctx context.Context, after int64, limit int) (string, []domain.MessageChange, error) {
	epoch, _, err := c.LastChange(ctx)
	if err != nil {
		return "", nil, err
	}
	type numbered struct {
		number int64
		room   domain.RoomID
		event  domain.EventID
	}
	list, err := collect(ctx, c.db, "the changes", `
		SELECT change, room_id, event_id FROM message_changes WHERE change > ? ORDER BY change LIMIT ?`,
		func(rows *sql.Rows) (n numbered, err error) {
			var room, event string
			err = rows.Scan(&n.number, &room, &event)
			n.room, n.event = domain.RoomID(room), domain.EventID(event)
			return n, err
		}, after, limit)
	if err != nil {
		return "", nil, err
	}
	out := make([]domain.MessageChange, 0, len(list))
	for _, n := range list {
		msg, ok, err := c.MessageByID(ctx, n.room, n.event)
		if err != nil {
			return "", nil, err
		}
		if ok { // gone since: a trim or a forgotten room
			out = append(out, domain.MessageChange{Number: n.number, Message: msg})
		}
	}
	return epoch, out, nil
}

// numberTouched gives each message tx touched (see messageChanges) its next number,
// in the order first touched, and reports whether it numbered any. A message deleted
// since it was touched gets none.
func numberTouched(ctx context.Context, tx *sql.Tx) (bool, error) {
	var touched bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM message_touched)").Scan(&touched); err != nil {
		return false, fmt.Errorf("db: read what changed: %w", err)
	}
	if !touched {
		return false, nil
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
		return false, fmt.Errorf("db: number what changed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: count what changed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE change_counter SET last = last + ?", n); err != nil {
		return false, fmt.Errorf("db: count the numbers given: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM message_touched"); err != nil {
		return false, fmt.Errorf("db: forget what was numbered: %w", err)
	}
	return n > 0, nil
}
