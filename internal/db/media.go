package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// message_media has two writers arriving at different times: the display half
// (with the message) and the fetch half (mxc + encrypted file JSON). Each upserts
// its own columns only.

// SaveMediaSource records where an attachment's bytes come from, for a cached message
// that is not redacted: a deleted message gains no content, so a late fetch cannot
// bring back what a redaction forgot. For a message the cache does not hold (history
// older than a full room keeps, trimmed as it arrived) it does nothing: there is
// nothing to show the attachment on.
func (c *Cache) SaveMediaSource(ctx context.Context, eventID domain.EventID, roomID domain.RoomID, mxc, fileJSON string) error {
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO message_media(room_id, event_id, mxc, file_json)
		SELECT ?1, ?2, ?3, ?4 WHERE EXISTS
			(SELECT 1 FROM messages WHERE room_id = ?1 AND event_id = ?2 AND redacted = 0)
		ON CONFLICT(room_id, event_id) DO UPDATE SET mxc=excluded.mxc, file_json=excluded.file_json`,
		string(roomID), string(eventID), mxc, fileJSON); err != nil {
		return fmt.Errorf("db: save media source %s: %w", eventID, err)
	}
	return nil
}

// MediaSource returns an attachment's mxc URI and file JSON; ok is false when no
// source is recorded. A Matrix attachment has an mxc URI (and file JSON when it is
// encrypted); another network's keeps only its own JSON, in file_json.
func (c *Cache) MediaSource(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (mxc, fileJSON string, ok bool, err error) {
	err = c.db.QueryRowContext(ctx,
		"SELECT mxc, file_json FROM message_media WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)).Scan(&mxc, &fileJSON)
	if ok, err = optional(err); !ok || (mxc == "" && fileJSON == "") {
		if err != nil {
			err = fmt.Errorf("db: lookup media source %s: %w", eventID, err)
		}
		return "", "", false, err
	}
	return mxc, fileJSON, true, nil
}

// saveMedia records the display half of an attachment. No attachment writes
// nothing: a re-save that knows less (failed decrypt) must not undo one that knew more.
func saveMedia(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, msg *domain.Message) error {
	if msg.Media == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_media(room_id, event_id, kind, name, mime, width, height, size)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(room_id, event_id) DO UPDATE SET
			kind = excluded.kind, name = excluded.name, mime = excluded.mime,
			width = excluded.width, height = excluded.height, size = excluded.size`,
		string(roomID), string(msg.ID), string(msg.Media.Type), msg.Media.Name,
		msg.Media.Mime, msg.Media.Width, msg.Media.Height, msg.Media.Size); err != nil {
		return fmt.Errorf("db: save media of %s: %w", msg.ID, err)
	}
	return nil
}

// saveExtras writes a message's side rows once its row is upserted: the edit it now
// shows (apply), its attachment and formatting, and for a redacted copy, who deleted
// it and, unless keep, forgetting what was cached.
func saveExtras(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message, apply, keep bool) error {
	if m.Redacted {
		if err := recordRedaction(ctx, tx, roomID, m.ID, m.RedactedBy, m.RedactedReason); err != nil {
			return err
		}
		if err := forgetEdit(ctx, tx, roomID, m.ID); err != nil {
			return err
		}
		if !keep {
			return forgetContent(ctx, tx, roomID, m.ID)
		}
		return nil
	}
	if apply {
		if err := recordEdit(ctx, tx, roomID, m); err != nil {
			return err
		}
	}
	if err := saveMedia(ctx, tx, roomID, m); err != nil {
		return err
	}
	return saveHTML(ctx, tx, roomID, m, apply)
}

// errDrawnOnly refuses formatting that came without its markup (a client's copy): the
// cache stores markup, and saving none would silently drop the message's formatting.
var errDrawnOnly = errors.New("db: formatting without markup cannot be stored")

// storable is formatting's stored form, refusing formatting that has none.
func storable(f richtext.Formatted, id domain.EventID) (string, error) {
	if f.Markup() == "" && !f.IsZero() {
		return "", fmt.Errorf("%w (%s)", errDrawnOnly, id)
	}
	return f.Markup(), nil
}

// saveHTML records a message's formatting, deleting the row when an edit removed
// it. Like the body, it is never written for an already-redacted row, and a copy
// that is not a newer edit (apply) never overwrites an edited row's formatting: the
// original arriving again, or an older edit delivered later.
func saveHTML(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message, apply bool) error {
	const kept = `EXISTS (SELECT 1 FROM messages WHERE room_id = ? AND event_id = ?
		AND (redacted = 1 OR (edited = 1 AND ? = 0)))`
	markup, err := storable(m.Format, m.ID)
	if err != nil {
		return err
	}
	if markup == "" {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM message_html WHERE room_id = ? AND event_id = ? AND NOT "+kept,
			string(roomID), string(m.ID), string(roomID), string(m.ID), apply); err != nil {
			return fmt.Errorf("db: clear message html %s: %w", m.ID, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_html(room_id, event_id, html)
		SELECT ?, ?, ? WHERE NOT `+kept+`
		ON CONFLICT(room_id, event_id) DO UPDATE SET html=excluded.html`,
		string(roomID), string(m.ID), markup, string(roomID), string(m.ID), apply); err != nil {
		return fmt.Errorf("db: save message html %s: %w", m.ID, err)
	}
	return nil
}
