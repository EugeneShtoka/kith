package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SenderMessages is a sender's cached messages in every room since since (all when
// zero), oldest first, at most limit; deleted ones are left out. Only what judging a
// sender needs is read: room, ID, words and time.
func (c *Cache) SenderMessages(ctx context.Context, sender string, since time.Time, limit int) ([]domain.Message, error) {
	var from int64
	if !since.IsZero() {
		from = since.UnixMilli()
	}
	return collect(ctx, c.db, "a sender's messages", `SELECT room_id, event_id, body, ts_ms FROM messages
		WHERE sender = ? AND ts_ms >= ? AND redacted = 0 ORDER BY ts_ms, event_id LIMIT ?`,
		func(rows *sql.Rows) (domain.Message, error) {
			var room, event, body string
			var ts int64
			err := rows.Scan(&room, &event, &body, &ts)
			return domain.Message{
				ID: domain.EventID(event), RoomID: domain.RoomID(room), Sender: sender, Body: body, Timestamp: time.UnixMilli(ts),
			}, err
		}, sender, from, limit)
}

// WrittenTo is everyone you have written to: the others in a direct chat you wrote in,
// and the senders of messages you replied to. me is every ID that is you.
func (c *Cache) WrittenTo(ctx context.Context, me []string) ([]string, error) {
	if len(me) == 0 {
		return nil, nil
	}
	var notMine, mine, replier, answered string
	var args []any
	notMine, args = inIDs(args, me)
	mine, args = inIDs(args, me)
	replier, args = inIDs(args, me)
	answered, args = inIDs(args, me)
	// #nosec G202 -- inIDs emits only placeholders; every ID is bound.
	return collect(ctx, c.db, "the people you wrote to", `
		SELECT DISTINCT t.sender FROM messages t JOIN rooms r ON r.id = t.room_id
		 WHERE r.is_direct = 1 AND t.sender NOT`+notMine+`
		   AND EXISTS (SELECT 1 FROM messages s WHERE s.room_id = t.room_id AND s.sender`+mine+`)
		UNION
		SELECT DISTINCT t.sender FROM messages s
		  JOIN messages t ON t.room_id = s.room_id AND t.event_id = s.reply_to
		 WHERE s.reply_to <> '' AND s.sender`+replier+` AND t.sender NOT`+answered,
		func(rows *sql.Rows) (string, error) {
			var s string
			err := rows.Scan(&s)
			return s, err
		}, args...)
}
