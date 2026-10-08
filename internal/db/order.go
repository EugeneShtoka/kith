package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// seqOf is a message m's place among the messages sharing its time
// (domain.Message.Seq), 0 when none was given: message_order holds only the given.
const seqOf = `COALESCE((SELECT o.seq FROM message_order o WHERE o.room_id = m.room_id AND o.event_id = m.event_id), 0)`

// newestFirstIn is the timeline's order (domain.CompareMessages) reversed, for a query
// over messages m. A network that numbers a room's messages (Telegram) gives them
// whole seconds and IDs that are the room's then the number, so within one second the
// shorter ID is the earlier and, at one length, text order is numeric order: by the
// ID's text alone, message 100 came before 99. One whose IDs carry no order
// (WhatsApp) gives the order itself, as seq.
const newestFirstIn = " m.ts_ms DESC, " + seqOf + " DESC, length(m.event_id) DESC, m.event_id DESC"

// saveOrder keeps m's Seq, when it has one. The first given stays: a message seen
// again (history after live, a retry) does not move.
func saveOrder(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message) error {
	if m.Seq == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_order(room_id, event_id, seq) VALUES(?, ?, ?)
		ON CONFLICT(room_id, event_id) DO NOTHING`, string(roomID), string(m.ID), m.Seq); err != nil {
		return fmt.Errorf("db: keep the order of %s: %w", m.ID, err)
	}
	return nil
}

// saveMentions keeps whom m mentions, when it mentions anyone. A copy without its
// mentions (an edit's echo) leaves those kept.
func saveMentions(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message) error {
	for _, mn := range m.Mentions {
		if mn.UserID == "" {
			continue // a room mention: drawn from the body as it is
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_mentions(room_id, event_id, user_id, name) VALUES(?, ?, ?, ?)
			ON CONFLICT(room_id, event_id, user_id) DO UPDATE SET name = excluded.name`,
			string(roomID), string(m.ID), mn.UserID, mn.Name); err != nil {
			return fmt.Errorf("db: keep the mentions of %s: %w", m.ID, err)
		}
	}
	return nil
}

// scanMentions reads messageSelect's mentions column: a JSON array of [user, name].
func scanMentions(column string) []domain.Mention {
	var pairs [][2]string
	if json.Unmarshal([]byte(column), &pairs) != nil || len(pairs) == 0 {
		return nil
	}
	out := make([]domain.Mention, len(pairs))
	for i, p := range pairs {
		out[i] = domain.Mention{UserID: p[0], Name: p[1]}
	}
	return out
}

// UnlistedNumberMentions is the messages in owner's rooms whose text has an "@" and a
// digit but that list no one as mentioned: cached before mentions were kept, by a
// network that writes a mention as "@" and a number (WhatsApp). Body and room only.
func (c *Cache) UnlistedNumberMentions(ctx context.Context, owner domain.RoomOwner) ([]domain.Message, error) {
	if owner == "" {
		return nil, nil
	}
	return collect(ctx, c.db, "unlisted number mentions", `
		SELECT m.room_id, m.event_id, m.body FROM messages m
		 WHERE substr(m.room_id, 1, length(?1)) = ?1 AND m.body GLOB '*@[0-9]*'
		   AND NOT EXISTS (SELECT 1 FROM message_mentions x WHERE x.room_id = m.room_id AND x.event_id = m.event_id)`,
		func(rows *sql.Rows) (domain.Message, error) {
			var room, event, body string
			err := rows.Scan(&room, &event, &body)
			return domain.Message{RoomID: domain.RoomID(room), ID: domain.EventID(event), Body: body}, err
		}, string(owner))
}

// AddMentions keeps who a cached message mentions; a message no longer cached is
// left alone.
func (c *Cache) AddMentions(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, mentions []domain.Mention) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		var cached int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE room_id = ? AND event_id = ?",
			string(roomID), string(eventID)).Scan(&cached); err != nil || cached == 0 {
			return err
		}
		return saveMentions(ctx, tx, roomID, &domain.Message{ID: eventID, Mentions: mentions})
	})
}
