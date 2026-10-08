package db

import (
	"context"
	"database/sql"
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
