package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Thread read positions (MSC3771 threaded receipts). The row with an empty root is
// the room's floor, which a thread without its own receipt inherits; a thread's
// effective position is the later of the two.

// threadFloor is the floor row's root_event, matching thread_root of unthreaded messages.
const threadFloor = ""

// SaveThreadRead moves one read position (a thread's, or the floor when root is
// empty) forward only: receipts echo back and arrive out of order.
func (c *Cache) SaveThreadRead(ctx context.Context, roomID domain.RoomID, root, event domain.EventID, ts int64) error {
	return c.execInRoom(ctx, roomID, "save thread read position in "+string(roomID), `
		INSERT INTO thread_read(room_id, root_event, read_event, read_ts_ms) VALUES(?, ?, ?, ?)
		ON CONFLICT(room_id, root_event) DO UPDATE SET
			read_event=excluded.read_event,
			read_ts_ms=excluded.read_ts_ms
		WHERE (excluded.read_ts_ms, excluded.read_event) > (thread_read.read_ts_ms, thread_read.read_event)`,
		string(roomID), string(root), string(event), ts)
}

// SeedThreadFloor gives a room a floor if it has none (never an update), so its
// whole thread history does not count as unread.
func (c *Cache) SeedThreadFloor(ctx context.Context, roomID domain.RoomID, event domain.EventID, ts int64) error {
	return c.execInRoom(ctx, roomID, "seed thread floor in "+string(roomID), `
		INSERT INTO thread_read(room_id, root_event, read_event, read_ts_ms) VALUES(?, ?, ?, ?)
		ON CONFLICT(room_id, root_event) DO NOTHING`,
		string(roomID), threadFloor, string(event), ts)
}

// MessageTS is when a cached event happened, and false when it is not cached (a
// receipt on an uncached event is left unrecorded).
func (c *Cache) MessageTS(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (int64, bool, error) {
	var ts int64
	found, err := optional(c.db.QueryRowContext(ctx,
		"SELECT ts_ms FROM messages WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)).Scan(&ts))
	if err != nil {
		return 0, false, fmt.Errorf("db: timestamp of %s: %w", eventID, err)
	}
	return ts, found, nil
}

// countThreadsQuery counts, per thread, unread replies: newer than the thread's
// receipt, the room floor, and your own newest reply in that thread, excluding your
// own and redacted ones; with one room, that room's threads. me is every MXID that is
// this person (see countUnreadQuery). Timestamps alone are compared (an event-ID
// tiebreak is a guess that can leave a thread permanently unread). m.event_id is a
// bare column beside max(), which SQLite takes from the max row: the newest unread
// reply. Most recent activity first per room, the root breaking a tie.
func countThreadsQuery(me []string, room domain.RoomID) (string, []any) {
	var notMine, mine string
	var args []any
	notMine, args = inIDs(args, me)
	mine, args = inIDs(args, me)
	// #nosec G202 -- inIDs emits only placeholders; every MXID is bound.
	query := `
	SELECT m.room_id, m.thread_root, count(*), sum(m.mentioned), max(m.ts_ms), m.event_id,
	       coalesce(r.body, '')
	  FROM messages m
	  LEFT JOIN thread_read t ON t.room_id = m.room_id AND t.root_event = m.thread_root
	  LEFT JOIN thread_read f ON f.room_id = m.room_id AND f.root_event = ''
	  LEFT JOIN messages r ON r.room_id = m.room_id AND r.event_id = m.thread_root
	 WHERE m.thread_root <> '' AND m.sender NOT` + notMine + ` AND m.redacted = 0
	   AND m.ts_ms > coalesce(t.read_ts_ms, 0)
	   AND m.ts_ms > coalesce(f.read_ts_ms, 0)
	   AND m.ts_ms > coalesce((SELECT max(s.ts_ms) FROM messages s
	                            WHERE s.room_id = m.room_id
	                              AND s.sender` + mine + `
	                              AND s.thread_root = m.thread_root), 0)`
	if room != "" {
		query += ` AND m.room_id = ?`
		args = append(args, string(room))
	}
	return query + ` GROUP BY m.room_id, m.thread_root ORDER BY m.room_id, max(m.ts_ms) DESC, m.thread_root`, args
}

// CountThreadUnread is one room's threads with anything unread.
func (c *Cache) CountThreadUnread(ctx context.Context, me []string, roomID domain.RoomID) ([]domain.ThreadUnread, error) {
	query, args := countThreadsQuery(me, roomID)
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: count unread threads in %s: %w", roomID, err)
	}
	byRoom, err := scanThreadUnread(rows)
	if err != nil {
		return nil, err
	}
	return byRoom[roomID], nil
}

// CountThreadUnreadAll is CountThreadUnread for every room; absent rooms have none.
func (c *Cache) CountThreadUnreadAll(ctx context.Context, me []string) (map[domain.RoomID][]domain.ThreadUnread, error) {
	query, args := countThreadsQuery(me, "")
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: count unread threads: %w", err)
	}
	return scanThreadUnread(rows)
}

// scanThreadUnread reads either form's rows into per-room lists.
func scanThreadUnread(rows *sql.Rows) (map[domain.RoomID][]domain.ThreadUnread, error) {
	defer func() { _ = rows.Close() }()

	out := make(map[domain.RoomID][]domain.ThreadUnread)
	for rows.Next() {
		var (
			roomID, root, latestID, title string
			unread, latest                int64
			mentions                      sql.NullInt64
		)
		if err := rows.Scan(&roomID, &root, &unread, &mentions, &latest, &latestID, &title); err != nil {
			return nil, fmt.Errorf("db: scan unread thread: %w", err)
		}
		out[domain.RoomID(roomID)] = append(out[domain.RoomID(roomID)], domain.ThreadUnread{
			Root:     domain.EventID(root),
			Unread:   int(unread),
			Mentions: int(mentions.Int64),
			LatestAt: time.UnixMilli(latest),
			Latest:   domain.EventID(latestID),
			Title:    title,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate unread threads: %w", err)
	}
	return out, nil
}

// Threads lists every thread with a cached reply in a room, newest activity first,
// with counts and the newest reply's sender. The root need not be cached.
func (c *Cache) Threads(ctx context.Context, me []string, roomID domain.RoomID) ([]domain.Thread, error) {
	// Bare columns beside max() come from the newest reply (see countThreadsQuery).
	rows, err := c.db.QueryContext(ctx, `
		SELECT m.thread_root, count(*), max(m.ts_ms), coalesce(r.body, ''),
		       m.event_id, m.sender, m.sender_name
		  FROM messages m
		  LEFT JOIN messages r ON r.room_id = m.room_id AND r.event_id = m.thread_root
		 WHERE m.room_id = ? AND m.thread_root <> ''
		 GROUP BY m.thread_root
		 ORDER BY max(m.ts_ms) DESC, m.thread_root`, string(roomID))
	if err != nil {
		return nil, fmt.Errorf("db: list threads in %s: %w", roomID, err)
	}
	out, err := scanThreads(rows, roomID)
	if err != nil {
		return nil, err
	}
	unread, err := c.CountThreadUnread(ctx, me, roomID)
	if err != nil {
		return nil, err
	}
	counts := make(map[domain.EventID]domain.ThreadUnread, len(unread))
	for _, u := range unread {
		counts[u.Root] = u
	}
	for i := range out {
		if u, ok := counts[out[i].Root]; ok {
			out[i].Unread, out[i].Mentions = u.Unread, u.Mentions
		}
	}
	return out, nil
}

// scanThreads reads the grouped rows into summaries.
func scanThreads(rows *sql.Rows, roomID domain.RoomID) ([]domain.Thread, error) {
	return collectRows(rows, "threads in "+string(roomID), func(rows *sql.Rows) (domain.Thread, error) {
		var (
			root, title                string
			latest, sender, senderName string
			count, ts                  int64
		)
		if err := rows.Scan(&root, &count, &ts, &title, &latest, &sender, &senderName); err != nil {
			return domain.Thread{}, err
		}
		return domain.Thread{
			Root:             domain.EventID(root),
			RoomID:           roomID,
			Count:            int(count),
			LatestAt:         time.UnixMilli(ts),
			Title:            title,
			Latest:           domain.EventID(latest),
			LatestSender:     sender,
			LatestSenderName: senderName,
		}, nil
	})
}

// SpokeInThread reports whether one of me (every MXID that is this person) sent the
// thread's root or any reply in it.
func (c *Cache) SpokeInThread(ctx context.Context, roomID domain.RoomID, root domain.EventID, me []string) (bool, error) {
	var spoke bool
	in, args := inIDs([]any{string(roomID)}, me)
	// #nosec G202 -- inIDs emits only placeholders; every MXID is bound.
	if err := c.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM messages
			 WHERE room_id = ? AND sender`+in+` AND (thread_root = ? OR event_id = ?))`,
		append(args, string(root), string(root))...).Scan(&spoke); err != nil {
		return false, fmt.Errorf("db: participation in thread %s: %w", root, err)
	}
	return spoke, nil
}
