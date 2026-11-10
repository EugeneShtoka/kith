package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// message_media has two writers at different times: the display half (with the
// message) and the fetch half (mxc + file JSON). Each upserts only its own columns.

// SaveMediaSource records where an attachment's bytes come from. It does nothing for
// a redacted message (a late fetch must not undo the redaction) or an uncached one.
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

// MediaSource returns an attachment's mxc URI and file JSON; ok is false when none is
// recorded. Matrix has an mxc URI (plus file JSON when encrypted); other networks
// keep only their own JSON in file_json.
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

// saveMedia records the display half of an attachment. No attachment writes nothing,
// so a re-save that knows less (failed decrypt) cannot undo one that knew more.
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

// saveExtras writes a message's side rows after its upsert: the shown edit, attachment,
// formatting, and for a redacted copy who deleted it (forgetting the text unless keep).
func saveExtras(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message, apply, keep bool) error {
	// A deleted message keeps its place.
	if err := saveOrder(ctx, tx, roomID, m); err != nil {
		return err
	}
	if m.Redacted {
		if err := recordRedaction(ctx, tx, roomID, m.ID, m.RedactedBy, m.RedactedReason); err != nil {
			return err
		}
		if err := recordRedactionTime(ctx, tx, roomID, m.ID, m.RedactedAt); err != nil {
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
	// An edit's attachment counts only where the edit does: a late older one must not win.
	if isEdit := m.RevisionID != "" && m.RevisionID != m.ID; !isEdit || apply {
		if err := saveMedia(ctx, tx, roomID, m); err != nil {
			return err
		}
	}
	if err := savePoll(ctx, tx, roomID, m.ID, m.Poll); err != nil {
		return err
	}
	if err := saveMentions(ctx, tx, roomID, m); err != nil {
		return err
	}
	return saveHTML(ctx, tx, roomID, m, apply)
}

// errDrawnOnly refuses formatting without its markup (a client's copy): saving it
// would silently drop the message's formatting.
var errDrawnOnly = errors.New("db: formatting without markup cannot be stored")

// storable is formatting's stored form, refusing formatting that has none.
func storable(f richtext.Formatted, id domain.EventID) (string, error) {
	if f.Markup() == "" && !f.IsZero() {
		return "", fmt.Errorf("%w (%s)", errDrawnOnly, id)
	}
	return f.Markup(), nil
}

// saveHTML records a message's formatting, deleting the row when an edit removed it.
// Like the body, it skips redacted rows, and only a newer edit (apply) overwrites an
// edited row's formatting.
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

// attachmentsQuery is every attachment the cache holds: a deleted message's only under
// [display.deleted] keep. One on a disappearing message is marked, its sender having
// asked for it to go, and one on a deleted (or disappeared) message too.
// shortcut: a deleted message's edit is forgotten, so a copy kept under an edit is not
// found once its message is deleted; look the edit up in message_revisions if it matters.
const attachmentsQuery = `
	SELECT mm.room_id, mm.event_id, COALESCE(me.revision_id, ''), mm.name, mm.mime,
		EXISTS (SELECT 1 FROM message_expiry x WHERE x.room_id = mm.room_id AND x.event_id = mm.event_id),
		m.redacted
	FROM message_media mm
	JOIN messages m ON m.room_id = mm.room_id AND m.event_id = mm.event_id
	LEFT JOIN message_edit me ON me.room_id = mm.room_id AND me.event_id = mm.event_id`

// Attachments lists the attachments to archive in room ("" for every room) on messages
// sent since and not deleted, oldest first: the first a network removes.
// shortcut: one query holds every row in memory; page it if a cache holds millions.
func (c *Cache) Attachments(ctx context.Context, roomID domain.RoomID, since time.Time) ([]domain.AttachmentRef, error) {
	rows, err := c.db.QueryContext(ctx, attachmentsQuery+`
		WHERE m.redacted = 0 AND (?1 = '' OR mm.room_id = ?1) AND m.ts_ms >= ?2
		ORDER BY m.ts_ms, mm.event_id`, string(roomID), since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("db: list attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.AttachmentRef
	for rows.Next() {
		var a domain.AttachmentRef
		if err := rows.Scan(&a.Room, &a.Event, &a.Revision, &a.Name, &a.Mime, &a.Timed, &a.Deleted); err != nil {
			return nil, fmt.Errorf("db: list attachments: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list attachments: %w", err)
	}
	return out, nil
}

// Attachment is one message's attachment; false when it has none, or its message was
// deleted and what it said not kept.
func (c *Cache) Attachment(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.AttachmentRef, bool, error) {
	var a domain.AttachmentRef
	err := c.db.QueryRowContext(ctx, attachmentsQuery+` WHERE mm.room_id = ? AND mm.event_id = ?`,
		string(roomID), string(eventID)).Scan(&a.Room, &a.Event, &a.Revision, &a.Name, &a.Mime, &a.Timed, &a.Deleted)
	ok, err := optional(err)
	if err != nil {
		return a, false, fmt.Errorf("db: lookup attachment %s: %w", eventID, err)
	}
	return a, ok, nil
}
