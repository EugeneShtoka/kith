package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Gap is a stretch of a room's history the cache skipped: a sync cut the room's
// timeline short, so what was said between Since and Until is missing until fetched.
type Gap struct {
	ID     int64
	RoomID domain.RoomID
	// Boundary is the newest message the cache held below the gap, at Since: reaching
	// it while paging back fills the gap.
	Boundary domain.EventID
	Since    time.Time
	// Until is the oldest message fetched above the gap so far.
	Until time.Time
	// Token is where paging backward resumes.
	Token string
	// Announce: what fills it is announced as it arrives (not a catch-up after a restart).
	Announce bool
	// Capped: paging stopped at its limit; the rest fills when scrolled into.
	Capped bool
}

// AddGap records a gap below until in a room, bounded by the newest message the cache
// holds; a room with nothing cached has nothing to fill up to, so no gap (false).
func (c *Cache) AddGap(ctx context.Context, roomID domain.RoomID, token string, until time.Time, announce bool) (bool, error) {
	added := false
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		var boundary string
		var since int64
		found, err := optional(tx.QueryRowContext(ctx,
			`SELECT event_id, ts_ms FROM messages WHERE room_id = ? ORDER BY ts_ms DESC, event_id DESC LIMIT 1`,
			string(roomID)).Scan(&boundary, &since))
		if err != nil {
			return fmt.Errorf("db: the newest message of %s: %w", roomID, err)
		}
		if !found {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO room_gaps(room_id, boundary, since_ms, until_ms, token, announce) VALUES(?, ?, ?, ?, ?, ?)`,
			string(roomID), boundary, since, max(until.UnixMilli(), since), token, announce); err != nil {
			return fmt.Errorf("db: record a gap in %s: %w", roomID, err)
		}
		added = true
		return nil
	})
	return added, err
}

// Gaps is every recorded gap, oldest recorded first.
func (c *Cache) Gaps(ctx context.Context) ([]Gap, error) {
	return collect(ctx, c.db, "gaps",
		`SELECT id, room_id, boundary, since_ms, until_ms, token, announce, capped FROM room_gaps ORDER BY id`,
		func(rows *sql.Rows) (Gap, error) {
			var g Gap
			var room, boundary string
			var since, until int64
			if err := rows.Scan(&g.ID, &room, &boundary, &since, &until, &g.Token, &g.Announce, &g.Capped); err != nil {
				return Gap{}, err
			}
			g.RoomID, g.Boundary = domain.RoomID(room), domain.EventID(boundary)
			g.Since, g.Until = time.UnixMilli(since), time.UnixMilli(until)
			return g, nil
		})
}

// MoveGap records how far a gap has been filled: paging resumes at token, and the
// oldest message fetched is until (never later than before). A dropped gap stays dropped.
func (c *Cache) MoveGap(ctx context.Context, id int64, token string, until time.Time, capped bool) error {
	if _, err := c.db.ExecContext(ctx,
		`UPDATE room_gaps SET token = ?, until_ms = max(since_ms, min(until_ms, ?)), capped = ? WHERE id = ?`,
		token, until.UnixMilli(), capped, id); err != nil {
		return fmt.Errorf("db: move gap %d: %w", id, err)
	}
	return nil
}

// DropGap forgets a gap: filled, or its room is gone.
func (c *Cache) DropGap(ctx context.Context, id int64) error {
	if _, err := c.db.ExecContext(ctx, `DELETE FROM room_gaps WHERE id = ?`, id); err != nil {
		return fmt.Errorf("db: drop gap %d: %w", id, err)
	}
	return nil
}

// DropGapsAbove forgets a room's gaps whose lower edge is at or after reached: paging
// back from the newest message has gone past them, so they are filled.
func (c *Cache) DropGapsAbove(ctx context.Context, roomID domain.RoomID, reached time.Time) error {
	if _, err := c.db.ExecContext(ctx, `DELETE FROM room_gaps WHERE room_id = ? AND since_ms >= ?`,
		string(roomID), reached.UnixMilli()); err != nil {
		return fmt.Errorf("db: drop the filled gaps of %s: %w", roomID, err)
	}
	return nil
}

// MessageTimes is when each of ids that the cache holds was sent, as the cache keeps
// it (the earliest time any copy carried: an edit cached before its original holds the
// edit's time until the original arrives).
func (c *Cache) MessageTimes(ctx context.Context, roomID domain.RoomID, ids []domain.EventID) (map[domain.EventID]time.Time, error) {
	out := map[domain.EventID]time.Time{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := inIDs([]any{string(roomID)}, ids)
	// #nosec G202 -- inIDs emits only placeholders or a bound json_each.
	rows, err := collect(ctx, c.db, "message times", `SELECT event_id, ts_ms FROM messages WHERE room_id = ? AND event_id`+in,
		func(rows *sql.Rows) (struct {
			id string
			ts int64
		}, error,
		) {
			var r struct {
				id string
				ts int64
			}
			err := rows.Scan(&r.id, &r.ts)
			return r, err
		}, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[domain.EventID(r.id)] = time.UnixMilli(r.ts)
	}
	return out, nil
}
