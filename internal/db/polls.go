package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message's poll is kept whole, as JSON, beside it: written with the message, and
// rewritten on its own as its results change (SetPoll). No poll writes nothing, so a
// copy that says less (a bridge's placeholder) keeps the one known.

// savePoll records a message's poll; none writes nothing.
func savePoll(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID, poll *domain.Poll) error {
	if poll == nil {
		return nil
	}
	data, err := json.Marshal(poll)
	if err != nil {
		return fmt.Errorf("db: encode the poll of %s: %w", eventID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_polls(room_id, event_id, poll) VALUES(?, ?, ?)
		ON CONFLICT(room_id, event_id) DO UPDATE SET poll = excluded.poll`,
		string(roomID), string(eventID), string(data)); err != nil {
		return fmt.Errorf("db: save the poll of %s: %w", eventID, err)
	}
	return nil
}

// SetPoll rewrites the poll of a cached message, as its results now stand; a message
// the cache does not hold, or one deleted, is left alone.
func (c *Cache) SetPoll(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, poll domain.Poll) error {
	data, err := json.Marshal(poll)
	if err != nil {
		return fmt.Errorf("db: encode the poll of %s: %w", eventID, err)
	}
	if _, err := c.db.ExecContext(ctx, `INSERT INTO message_polls(room_id, event_id, poll)
		SELECT ?1, ?2, ?3 WHERE EXISTS
			(SELECT 1 FROM messages WHERE room_id = ?1 AND event_id = ?2 AND redacted = 0)
		ON CONFLICT(room_id, event_id) DO UPDATE SET poll = excluded.poll`,
		string(roomID), string(eventID), string(data)); err != nil {
		return fmt.Errorf("db: set the poll of %s: %w", eventID, err)
	}
	return nil
}

// PollsByID is the cached messages whose poll a network names by id, within an
// account's rooms: a results update names the poll, not the message.
func (c *Cache) PollsByID(ctx context.Context, owner domain.RoomOwner, id string) ([]domain.Message, error) {
	return collect(ctx, c.db, "polls by id", `
		SELECT room_id, event_id, poll FROM message_polls
		 WHERE substr(room_id, 1, length(?1)) = ?1 AND json_extract(poll, '$.ID') = ?2`,
		func(rows *sql.Rows) (domain.Message, error) {
			var room, event, data string
			if err := rows.Scan(&room, &event, &data); err != nil {
				return domain.Message{}, err
			}
			var p domain.Poll
			if err := json.Unmarshal([]byte(data), &p); err != nil {
				return domain.Message{}, fmt.Errorf("db: decode a poll: %w", err)
			}
			return domain.Message{RoomID: domain.RoomID(room), ID: domain.EventID(event), Poll: &p}, nil
		}, string(owner), id)
}
