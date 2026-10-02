package db

import (
	"context"
	"database/sql"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// RecentBodies is the bodies of the newest n unredacted messages: in rooms (none when
// it names none), and only senders' when senders names any (this person's IDs, on
// every network). What completion counts
// recent words from (internal/vocab); each shape is one index walk, newest first.
func (c *Cache) RecentBodies(ctx context.Context, rooms domain.RoomSet, senders []string, n int) ([]string, error) {
	if n <= 0 || rooms.None() {
		return nil, nil
	}
	where, args := []string{"redacted = 0"}, []any{}
	if !rooms.All {
		var in string
		in, args = inIDs(args, rooms.IDs)
		where = append(where, "room_id"+in)
	}
	if len(senders) > 0 {
		var in string
		in, args = inIDs(args, senders)
		where = append(where, "sender"+in)
	}
	args = append(args, n)
	return collect(ctx, c.db, "recent bodies",
		`SELECT body FROM messages WHERE `+strings.Join(where, " AND ")+` ORDER BY ts_ms DESC LIMIT ?`,
		func(rows *sql.Rows) (string, error) {
			var body string
			err := rows.Scan(&body)
			return body, err
		}, args...)
}
