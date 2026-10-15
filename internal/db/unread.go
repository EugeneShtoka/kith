package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Unread returns every room's cached unread state.
func (c *Cache) Unread(ctx context.Context) ([]domain.Unread, error) {
	return collect(ctx, c.db, "unread",
		"SELECT room_id, notifications, highlights, read_event, marked FROM room_unread",
		func(rows *sql.Rows) (domain.Unread, error) {
			var (
				roomID, readEvent         string
				notifications, highlights int
				marked                    bool
			)
			err := rows.Scan(&roomID, &notifications, &highlights, &readEvent, &marked)
			return domain.Unread{
				RoomID:        domain.RoomID(roomID),
				Notifications: notifications,
				Highlights:    highlights,
				ReadEvent:     domain.EventID(readEvent),
				Marked:        marked,
			}, err
		})
}

// SaveUnread writes one room's merged unread state. readTS (0: unknown) is the
// newest message time at or before the receipt in stream order, not the receipt
// event's own (see matrix.resolveReceipt). Counts and mark are replaced; the
// position only moves forward by readTS, like the server's receipts, since echoes
// and per-kind receipts arrive out of order.
func (c *Cache) SaveUnread(ctx context.Context, u domain.Unread, readTS int64) error {
	return c.execInRoom(ctx, u.RoomID, "upsert unread "+string(u.RoomID), `
		INSERT INTO room_unread(room_id, notifications, highlights, read_event, marked, read_ts_ms)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id) DO UPDATE SET
			notifications=excluded.notifications,
			highlights=excluded.highlights,
			read_event=CASE WHEN excluded.read_ts_ms >= room_unread.read_ts_ms
			                THEN excluded.read_event ELSE room_unread.read_event END,
			read_ts_ms=max(excluded.read_ts_ms, room_unread.read_ts_ms),
			marked=excluded.marked`,
		string(u.RoomID), u.Notifications, u.Highlights, string(u.ReadEvent), u.Marked, readTS)
}

// ReadPositions is when each room's read event happened: the stored timestamp, or
// the cached event's when the row predates it. Rooms with neither are absent.
func (c *Cache) ReadPositions(ctx context.Context) (map[domain.RoomID]int64, error) {
	return collectMap(ctx, c.db, "read positions", `
		SELECT u.room_id, max(u.read_ts_ms, coalesce(r.ts_ms, 0)) AS ts
		  FROM room_unread u
		  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event
		 WHERE u.read_event <> '' AND (u.read_ts_ms > 0 OR r.event_id IS NOT NULL)`,
		func(rows *sql.Rows) (domain.RoomID, int64, error) {
			var (
				roomID string
				ts     int64
			)
			err := rows.Scan(&roomID, &ts)
			return domain.RoomID(roomID), ts, err
		})
}

// readPosition is a room's read position over room_unread u and its LEFT JOINed
// read event r: read_ts_ms, or the cached event's time for a row that predates it.
const readPosition = `max(u.read_ts_ms, coalesce(r.ts_ms, 0))`

// readKnown keeps the rooms whose read position is known.
const readKnown = ` (u.read_ts_ms > 0 OR r.event_id IS NOT NULL)`

// countUnreadQuery counts unread main-timeline messages and mentions per room (or
// for one room). me is every MXID that is this person. Rooms with an unknown read
// position are dropped (the caller uses the server count); placeholders count too.
// Own, redacted and threaded messages are excluded, and hidden people's with the
// direct replies to them (see notHidden). Only ts_ms is compared: an
// event-ID tiebreak can leave a room permanently unread. The floor is the later of
// the receipt and your newest message, one index seek per MXID.
func countUnreadQuery(me, hidden []string, room domain.RoomID) (string, []any) {
	var args []any
	count := func(mentioned string) string {
		var notMine, mine, visible string
		notMine, args = inIDs(args, me)
		visible, args = notHidden(hidden, args)
		mine, args = inIDs(args, me)
		return `(SELECT count(*) FROM messages m
		         WHERE m.room_id = u.room_id AND m.sender NOT` + notMine + visible + ` AND m.redacted = 0
		           AND m.thread_root = ''` + mentioned + `
		           AND m.ts_ms > max(` + readPosition + `, coalesce(
		               (SELECT max(s.ts_ms) FROM messages s
		                 WHERE s.room_id = u.room_id AND s.sender` + mine + ` AND s.thread_root = ''), 0)))`
	}
	// #nosec G202 -- inIDs emits only placeholders; every MXID is bound.
	query := `
	SELECT u.room_id, ` + count("") + `, ` + count(" AND m.mentioned = 1") + `
	  FROM room_unread u
	  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event
	 WHERE` + readKnown
	if room != "" {
		query += ` AND u.room_id = ?`
		args = append(args, string(room))
	}
	return query, args
}

// CountUnread counts one room's unread main-timeline messages and mentions;
// counted is false when its read position is unknown.
func (c *Cache) CountUnread(ctx context.Context, me []string, roomID domain.RoomID) (messages, mentions int, counted bool, err error) {
	query, args := countUnreadQuery(me, c.hiddenSenders(), roomID)
	row := c.db.QueryRowContext(ctx, query, args...)
	var id string
	if counted, err = optional(row.Scan(&id, &messages, &mentions)); !counted {
		if err != nil {
			err = fmt.Errorf("db: count unread %s: %w", roomID, err)
		}
		return 0, 0, false, err
	}
	return messages, mentions, true, nil
}

// CountUnreadAll is CountUnread for every room it can answer, as partial Unread
// values the caller merges onto the server's.
func (c *Cache) CountUnreadAll(ctx context.Context, me []string) (map[domain.RoomID]domain.Unread, error) {
	query, args := countUnreadQuery(me, c.hiddenSenders(), "")
	return collectMap(ctx, c.db, "unread counts", query,
		func(rows *sql.Rows) (domain.RoomID, domain.Unread, error) {
			var (
				roomID             string
				messages, mentions int
			)
			err := rows.Scan(&roomID, &messages, &mentions)
			return domain.RoomID(roomID), domain.Unread{
				RoomID:   domain.RoomID(roomID),
				Messages: messages,
				Mentions: mentions,
				Counted:  true,
			}, err
		}, args...)
}
