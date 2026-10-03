package db

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A reply on a network that sends what it quotes (WhatsApp) keeps the quoted
// message's sender and words, for when the message itself is not cached: older than
// the history a room keeps, or from before the account was linked. It is no
// message: it has no time, and never shows in the timeline, only in the reply.

// SaveQuoted keeps what a reply in roomID quotes of eventID. It does nothing for a
// room the cache does not hold.
func (c *Cache) SaveQuoted(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, sender, body string) error {
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO quoted_messages(room_id, event_id, sender, body)
		SELECT ?1, ?2, ?3, ?4 WHERE EXISTS (SELECT 1 FROM rooms WHERE id = ?1)
		ON CONFLICT(room_id, event_id) DO UPDATE SET sender=excluded.sender, body=excluded.body`,
		string(roomID), string(eventID), sender, body); err != nil {
		return fmt.Errorf("db: save quoted %s: %w", eventID, err)
	}
	return nil
}

// Quoted is what a reply quoted of eventID, as a message with no time; ok is false
// when no reply quoted it.
func (c *Cache) Quoted(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (msg domain.Message, ok bool, err error) {
	err = c.db.QueryRowContext(ctx,
		"SELECT sender, body FROM quoted_messages WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)).Scan(&msg.Sender, &msg.Body)
	if ok, err = optional(err); !ok {
		if err != nil {
			err = fmt.Errorf("db: read quoted %s: %w", eventID, err)
		}
		return domain.Message{}, false, err
	}
	msg.ID, msg.RoomID = eventID, roomID
	return msg, true, nil
}
