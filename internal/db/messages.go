package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// messagesPerRoom bounds the messages kept per room; SaveMessages trims to it.
const messagesPerRoom = 2000

// Messages returns up to limit of a room's newest cached messages, oldest first.
func (c *Cache) Messages(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Message, error) {
	msgs, err := collect(ctx, c.db, "messages",
		messageSelect+` WHERE m.room_id = ? ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?`,
		scanMessage(roomID), string(roomID), limit)
	if err != nil {
		return nil, err
	}
	slices.Reverse(msgs)
	return msgs, nil
}

// MessageByID is one cached message, read as Messages reads each.
func (c *Cache) MessageByID(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, bool, error) {
	msgs, err := collect(ctx, c.db, "message",
		messageSelect+` WHERE m.room_id = ? AND m.event_id = ?`,
		scanMessage(roomID), string(roomID), string(eventID))
	if err != nil || len(msgs) == 0 {
		return domain.Message{}, false, err
	}
	return msgs[0], true, nil
}

// MarkRedacted flags a cached message as redacted, keeping sender/ts for the
// placeholder, and erases its content unless keep. A message not cached yet gets a
// tombstone instead (at is when it was deleted): whatever copy arrives later is saved
// as deleted (see applyTombstone).
func (c *Cache) MarkRedacted(
	ctx context.Context, roomID domain.RoomID, eventID domain.EventID, by, reason string, at time.Time, keep bool,
) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE messages SET redacted = 1 WHERE room_id = ? AND event_id = ?",
			string(roomID), string(eventID))
		if err != nil {
			return fmt.Errorf("db: mark redacted %s: %w", eventID, err)
		}
		if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
			// Not a cached message: maybe the edit one shows (edits fold onto their
			// target). Either way its tombstone keeps a later copy from applying.
			if err := revertShownEdit(ctx, tx, roomID, eventID, keep); err != nil {
				return err
			}
			return buryUncached(ctx, tx, roomID, eventID, by, reason, at)
		}
		if err := forgetEdit(ctx, tx, roomID, eventID); err != nil {
			return err
		}
		if !keep {
			if err := forgetContent(ctx, tx, roomID, eventID); err != nil {
				return err
			}
		}
		if by == "" && reason == "" {
			return nil
		}
		return recordRedaction(ctx, tx, roomID, eventID, by, reason)
	})
}

// buryUncached records the tombstone of a redaction whose message is not cached.
func buryUncached(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID, by, reason string, at time.Time) error {
	if err := registerRoom(ctx, tx, roomID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_tombstone(room_id, event_id, by, reason, ts_ms) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(room_id, event_id) DO UPDATE SET by=excluded.by, reason=excluded.reason, ts_ms=excluded.ts_ms`,
		string(roomID), string(eventID), by, reason, at.UnixMilli()); err != nil {
		return fmt.Errorf("db: record the redaction of uncached %s: %w", eventID, err)
	}
	return nil
}

// isBuried reports whether a redaction of event arrived before any copy of it (see
// buryUncached): for an edit event, that it was deleted.
func isBuried(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, event domain.EventID) (bool, error) {
	if event == "" {
		return false, nil
	}
	var one int
	found, err := optional(tx.QueryRowContext(ctx,
		"SELECT 1 FROM message_tombstone WHERE room_id = ? AND event_id = ?",
		string(roomID), string(event)).Scan(&one))
	if err != nil {
		return false, fmt.Errorf("db: read the tombstone of %s: %w", event, err)
	}
	return found, nil
}

// applyTombstone saves a just-upserted message as deleted when a redaction of it came
// first: flagged, its content forgotten (nothing of it was cached when it was deleted,
// so there is nothing to keep), who deleted it recorded, and the tombstone spent.
// applied reports whether there was one.
func applyTombstone(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID) (applied bool, err error) {
	var by, reason string
	found, err := optional(tx.QueryRowContext(ctx,
		"SELECT by, reason FROM message_tombstone WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)).Scan(&by, &reason))
	if err != nil {
		return false, fmt.Errorf("db: read tombstone of %s: %w", eventID, err)
	}
	if !found {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE messages SET redacted = 1 WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)); err != nil {
		return false, fmt.Errorf("db: mark redacted %s: %w", eventID, err)
	}
	if err := forgetContent(ctx, tx, roomID, eventID); err != nil {
		return false, err
	}
	if err := recordRedaction(ctx, tx, roomID, eventID, by, reason); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM message_tombstone WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)); err != nil {
		return false, fmt.Errorf("db: spend tombstone of %s: %w", eventID, err)
	}
	return true, nil
}

// recordRedaction keeps who deleted a cached message and why; nothing for one not
// cached (the stripped copy the server later serves carries them).
func recordRedaction(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID, by, reason string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_redaction(room_id, event_id, by, reason)
		SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE room_id = ? AND event_id = ?)
		ON CONFLICT(room_id, event_id) DO UPDATE SET by=excluded.by, reason=excluded.reason`,
		string(roomID), string(eventID), by, reason, string(roomID), string(eventID)); err != nil {
		return fmt.Errorf("db: record redaction %s: %w", eventID, err)
	}
	return nil
}

// forgetContent erases a redacted message's body, formatting, media and revisions.
func forgetContent(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID) error {
	for _, stmt := range []string{
		"UPDATE messages SET body = '' WHERE room_id = ? AND event_id = ?",
		"DELETE FROM message_html WHERE room_id = ? AND event_id = ?",
		"DELETE FROM message_media WHERE room_id = ? AND event_id = ?",
		"DELETE FROM message_revisions WHERE room_id = ? AND event_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, string(roomID), string(eventID)); err != nil {
			return fmt.Errorf("db: forget redacted content %s: %w", eventID, err)
		}
	}
	return nil
}

// EditShownBy is the message whose body comes from edit rev, if one does.
func (c *Cache) EditShownBy(ctx context.Context, roomID domain.RoomID, rev domain.EventID) (domain.EventID, bool, error) {
	var target string
	found, err := optional(c.db.QueryRowContext(ctx,
		"SELECT event_id FROM message_edit WHERE room_id = ? AND revision_id = ?",
		string(roomID), string(rev)).Scan(&target))
	if err != nil {
		return "", false, fmt.Errorf("db: find the message edit %s shows on: %w", rev, err)
	}
	return domain.EventID(target), found, nil
}

// revertShownEdit takes a deleted edit's words off the message showing them: back to
// the newest other version cached (under keep), else to nothing and unedited, so the
// original or another edit arriving later fills it (and the daemon asks the server).
func revertShownEdit(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, rev domain.EventID, keep bool) error {
	var target string
	found, err := optional(tx.QueryRowContext(ctx,
		"SELECT event_id FROM message_edit WHERE room_id = ? AND revision_id = ?",
		string(roomID), string(rev)).Scan(&target))
	if err != nil || !found {
		return err
	}
	var prev domain.Revision
	var prevID, prevMarkup string
	var prevTS int64
	have := false
	if keep {
		have, err = optional(tx.QueryRowContext(ctx, `
			SELECT v.revision_id, v.body, v.html, v.ts_ms FROM message_revisions v
			 WHERE v.room_id = ?1 AND v.event_id = ?2 AND v.revision_id <> ?3
			   AND NOT EXISTS (SELECT 1 FROM message_tombstone t
			                    WHERE t.room_id = ?1 AND t.event_id = v.revision_id)
			 ORDER BY v.ts_ms DESC, v.revision_id DESC LIMIT 1`,
			string(roomID), target, string(rev)).Scan(&prevID, &prev.Body, &prevMarkup, &prevTS))
		if err != nil {
			return fmt.Errorf("db: read the version before %s: %w", rev, err)
		}
	}
	prev.ID, prev.At = domain.EventID(prevID), time.UnixMilli(prevTS)
	prev.Format = richtext.FromMarkup(prevMarkup)
	return showVersion(ctx, tx, roomID, domain.EventID(target), prev, have)
}

// showVersion makes a message show one version of itself: an edit (recorded as the one
// shown), the original (unedited), or, with none, nothing until one arrives.
func showVersion(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, target domain.EventID, v domain.Revision, have bool) error {
	edited := have && v.ID != target
	if _, err := tx.ExecContext(ctx,
		"UPDATE messages SET body = ?, edited = ? WHERE room_id = ? AND event_id = ? AND redacted = 0",
		v.Body, edited, string(roomID), string(target)); err != nil {
		return fmt.Errorf("db: show a version of %s: %w", target, err)
	}
	if err := forgetEdit(ctx, tx, roomID, target); err != nil {
		return err
	}
	if edited {
		m := domain.Message{ID: target, RevisionID: v.ID, EditedAt: v.At}
		if err := recordEdit(ctx, tx, roomID, &m); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM message_html WHERE room_id = ? AND event_id = ?",
		string(roomID), string(target)); err != nil {
		return fmt.Errorf("db: clear the formatting of %s: %w", target, err)
	}
	if markup := v.Format.Markup(); markup != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO message_html(room_id, event_id, html) SELECT ?, ?, ? WHERE EXISTS
				(SELECT 1 FROM messages WHERE room_id = ? AND event_id = ? AND redacted = 0)`,
			string(roomID), string(target), markup, string(roomID), string(target)); err != nil {
			return fmt.Errorf("db: restore the formatting of %s: %w", target, err)
		}
	}
	return nil
}

// ShowVersion is showVersion for the daemon, which fetched the version from the server
// after the cache had none: taken only while the message still shows nothing.
func (c *Cache) ShowVersion(ctx context.Context, roomID domain.RoomID, target domain.EventID, v domain.Revision) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		var body string
		var redacted bool
		found, err := optional(tx.QueryRowContext(ctx,
			"SELECT body, redacted FROM messages WHERE room_id = ? AND event_id = ?",
			string(roomID), string(target)).Scan(&body, &redacted))
		if err != nil {
			return fmt.Errorf("db: read %s: %w", target, err)
		}
		if !found || redacted || body != "" {
			return nil // gone, deleted, or filled by a copy that arrived meanwhile
		}
		return showVersion(ctx, tx, roomID, target, v, true)
	})
}

// forgetEdit drops the record of which edit a message shows, once it is deleted: a
// deleted message says it was edited, not when or by which edit (as the client's
// merge has it, see domain.combine).
func forgetEdit(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, eventID domain.EventID) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM message_edit WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)); err != nil {
		return fmt.Errorf("db: forget the edit of %s: %w", eventID, err)
	}
	return nil
}

// revisionsOf is the version each message carries, keyed by the event that carried
// it; redactions and empty messages carry none.
func revisionsOf(msgs []domain.Message) []revisionRow {
	rows := make([]revisionRow, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		if m.Redacted || (m.Body == "" && m.Format.IsZero()) {
			continue
		}
		revision := m.RevisionID
		if revision == "" {
			revision = m.ID
		}
		rows = append(rows, revisionRow{
			event: m.ID,
			rev:   domain.Revision{ID: revision, Body: m.Body, Format: m.Format, At: m.Timestamp},
		})
	}
	return rows
}

// SaveRevisionsFor records versions fetched for one message (the backfill path).
func (c *Cache) SaveRevisionsFor(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, revs []domain.Revision) error {
	rows := make([]revisionRow, 0, len(revs))
	for _, rev := range revs {
		if rev.ID == "" || (rev.Body == "" && rev.Format.IsZero()) {
			continue
		}
		rows = append(rows, revisionRow{event: eventID, rev: rev})
	}
	return c.insertRevisions(ctx, roomID, rows)
}

type revisionRow struct {
	event domain.EventID
	rev   domain.Revision
}

// insertRevisions writes rows in one transaction, ignoring existing ones.
func (c *Cache) insertRevisions(ctx context.Context, roomID domain.RoomID, rows []revisionRow) error {
	if len(rows) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error { return writeRevisions(ctx, tx, roomID, rows) })
}

// writeRevisions is insertRevisions inside the caller's transaction. A redacted
// message gains no versions (its kept ones stay, and a forgotten one stays empty),
// nor does one not cached (a copy of a deleted edit is not saved), and a deleted edit
// is not a version.
func writeRevisions(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, rows []revisionRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO message_revisions(room_id, event_id, revision_id, body, html, ts_ms)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6
		 WHERE EXISTS (SELECT 1 FROM messages WHERE room_id = ?1 AND event_id = ?2 AND redacted = 0)
		   AND NOT EXISTS (SELECT 1 FROM message_tombstone WHERE room_id = ?1 AND event_id = ?3)
		ON CONFLICT(room_id, event_id, revision_id) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("db: prepare revision insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range rows {
		row := &rows[i]
		markup, err := storable(row.rev.Format, row.rev.ID)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, string(roomID), string(row.event), string(row.rev.ID),
			row.rev.Body, markup, row.rev.At.UnixMilli()); err != nil {
			return fmt.Errorf("db: save revision %s: %w", row.rev.ID, err)
		}
	}
	return nil
}

// Revisions returns every kept version of one message, oldest first.
func (c *Cache) Revisions(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, error) {
	return collect(ctx, c.db, "revisions",
		`SELECT revision_id, body, html, ts_ms FROM message_revisions
		  WHERE room_id = ? AND event_id = ? ORDER BY ts_ms ASC, revision_id ASC`,
		func(rows *sql.Rows) (domain.Revision, error) {
			var rev domain.Revision
			var id, body, html string
			var ts int64
			if err := rows.Scan(&id, &body, &html, &ts); err != nil {
				return domain.Revision{}, err
			}
			rev.ID, rev.Body, rev.Format, rev.At = domain.EventID(id), body, richtext.FromMarkup(html), time.UnixMilli(ts)
			return rev, nil
		}, string(roomID), string(eventID))
}

// SaveMessages upserts messages (skipping ones without an event ID) and trims the
// room to messagesPerRoom, in one transaction. A redacted copy (the server strips
// content) forgets what the cache held, as MarkRedacted without keep does.
func (c *Cache) SaveMessages(ctx context.Context, roomID domain.RoomID, msgs []domain.Message) error {
	return c.saveMessages(ctx, roomID, msgs, false)
}

// SaveMessagesWithRevisions is SaveMessages for [display.deleted] keep: it also
// records the version each message carries, for the history view, in the same
// transaction (a crash cannot keep a message and lose the version it arrived as),
// and a redacted copy keeps the words already cached.
func (c *Cache) SaveMessagesWithRevisions(ctx context.Context, roomID domain.RoomID, msgs []domain.Message) error {
	return c.saveMessages(ctx, roomID, msgs, true)
}

func (c *Cache) saveMessages(ctx context.Context, roomID domain.RoomID, msgs []domain.Message, keep bool) error {
	var revisions []revisionRow
	if keep {
		revisions = revisionsOf(msgs)
	}
	mine := c.mine() // outside the transaction: the networks answer it under their own locks
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		// Order-independent upsert: flags only go 0→1, ts keeps the earliest, and a
		// redacted body is never rewritten. A redacted copy empties the body unless
		// keep (?13); an edit's body wins only when it is newer than the one shown
		// (?14, see domain.SupersedesEdit). reply_to and thread_root are never
		// blanked (an edit carries an empty thread root).
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO messages(room_id, event_id, sender, sender_name, body, ts_ms, redacted, edited, reply_to, mentioned, thread_root, emote) VALUES(?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)
			ON CONFLICT(room_id, event_id) DO UPDATE SET
				sender=excluded.sender,
				sender_name=excluded.sender_name,
				body=CASE WHEN messages.redacted=1 THEN messages.body
				          WHEN excluded.redacted=1 AND ?13 THEN messages.body
				          WHEN excluded.redacted=1 THEN excluded.body
				          WHEN ?14 THEN excluded.body
				          WHEN messages.edited=1 THEN messages.body
				          ELSE excluded.body END,
				ts_ms=min(messages.ts_ms, excluded.ts_ms),
				redacted=max(messages.redacted, excluded.redacted),
				edited=max(messages.edited, excluded.edited),
				reply_to=CASE WHEN excluded.reply_to != '' THEN excluded.reply_to ELSE messages.reply_to END,
				mentioned=max(messages.mentioned, excluded.mentioned),
				thread_root=CASE WHEN excluded.thread_root != '' THEN excluded.thread_root ELSE messages.thread_root END,
				emote=max(messages.emote, excluded.emote)`)
		if err != nil {
			return fmt.Errorf("db: prepare message upsert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for i := range msgs {
			if msgs[i].ID == "" {
				continue
			}
			if err := saveMessage(ctx, tx, stmt, roomID, &msgs[i], keep); err != nil {
				return err
			}
		}
		if err := dropSuperseded(ctx, tx, roomID, msgs); err != nil {
			return err
		}
		if err := writeRevisions(ctx, tx, roomID, revisions); err != nil {
			return err
		}
		return c.trim(ctx, tx, roomID, mine)
	})
}

// saveMessage upserts one message with stmt (saveMessages' upsert), then its side
// rows: the edit it shows, or, when a redaction of it came first, its deletion.
func saveMessage(ctx context.Context, tx *sql.Tx, stmt *sql.Stmt, roomID domain.RoomID, m *domain.Message, keep bool) error {
	if m.RevisionID != "" && m.RevisionID != m.ID {
		// A copy of a deleted edit adds nothing: not its words to a cached row, and
		// not a row of its words for an uncached one.
		if buried, err := isBuried(ctx, tx, roomID, m.RevisionID); err != nil || buried {
			return err
		}
	}
	apply, err := editApplies(ctx, tx, roomID, m)
	if err != nil {
		return err
	}
	if _, err = stmt.ExecContext(ctx, string(roomID), string(m.ID), m.Sender, m.SenderName,
		m.Body, m.Timestamp.UnixMilli(), m.Redacted, m.Edited, string(m.ReplyTo),
		m.Mentioned, string(m.ThreadRoot), m.Emote, keep, apply); err != nil {
		return fmt.Errorf("db: upsert message %s: %w", m.ID, err)
	}
	buried, err := applyTombstone(ctx, tx, roomID, m.ID)
	if err != nil || buried {
		return err // buried: deleted before it arrived, so none of it is kept
	}
	return saveExtras(ctx, tx, roomID, m, apply, keep)
}

// trim keeps a room's newest messagesPerRoom by a timestamp cutoff (one index seek; no
// cutoff under the cap). Messages sharing the boundary timestamp are all kept. Others'
// reactions to what is trimmed go with it: nothing shows them, and they were the one
// table that grew without bound. Our own stay, since reaction emoji are ranked from
// them, and a reaction whose target was never cached is not touched (it may be paged
// in later).
func (c *Cache) trim(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, mine []string) error {
	var cutoff sql.NullInt64
	found, err := optional(tx.QueryRowContext(ctx,
		`SELECT ts_ms FROM messages WHERE room_id = ? ORDER BY ts_ms DESC LIMIT 1 OFFSET ?`,
		string(roomID), messagesPerRoom-1).Scan(&cutoff))
	if err != nil {
		return fmt.Errorf("db: trim cutoff: %w", err)
	}
	if !found || !cutoff.Valid {
		return nil
	}
	if len(mine) > 0 {
		in, args := inIDs([]any{string(roomID)}, mine)
		args = append(args, string(roomID), cutoff.Int64)
		// #nosec G202 -- inIDs emits only placeholders or a bound json_each.
		query := `
			DELETE FROM reactions
			WHERE room_id = ? AND sender NOT` + in + `
			  AND target_event IN (SELECT event_id FROM messages WHERE room_id = ? AND ts_ms < ?)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("db: trim reactions: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM messages WHERE room_id = ? AND ts_ms < ?`, string(roomID), cutoff.Int64); err != nil {
		return fmt.Errorf("db: trim messages: %w", err)
	}
	// A message is older than its deletion, so one deleted before the cutoff would be
	// trimmed on arrival anyway: its tombstone guards nothing.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM message_tombstone WHERE room_id = ? AND ts_ms < ?`, string(roomID), cutoff.Int64); err != nil {
		return fmt.Errorf("db: trim tombstones: %w", err)
	}
	return nil
}

// editApplies reports whether m is an edit newer than the one its target shows, so
// its body and formatting replace what is cached.
func editApplies(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message) (bool, error) {
	if !m.Edited || m.Redacted {
		return false, nil
	}
	if buried, err := isBuried(ctx, tx, roomID, m.RevisionID); err != nil || buried {
		return false, err // a deleted edit
	}
	var rev string
	var ts int64
	found, err := optional(tx.QueryRowContext(ctx,
		"SELECT revision_id, ts_ms FROM message_edit WHERE room_id = ? AND event_id = ?",
		string(roomID), string(m.ID)).Scan(&rev, &ts))
	if err != nil {
		return false, fmt.Errorf("db: read edit of %s: %w", m.ID, err)
	}
	if !found {
		return true, nil
	}
	return domain.SupersedesEdit(*m, time.UnixMilli(ts), domain.EventID(rev)), nil
}

// recordEdit notes the edit a message now shows; nothing once it is redacted.
func recordEdit(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, m *domain.Message) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_edit(room_id, event_id, revision_id, ts_ms)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS
			(SELECT 1 FROM messages WHERE room_id = ? AND event_id = ? AND redacted = 1)
		ON CONFLICT(room_id, event_id) DO UPDATE SET revision_id=excluded.revision_id, ts_ms=excluded.ts_ms`,
		string(roomID), string(m.ID), string(m.RevisionID), m.EditTime().UnixMilli(),
		string(roomID), string(m.ID)); err != nil {
		return fmt.Errorf("db: record edit of %s: %w", m.ID, err)
	}
	return nil
}

// dropSuperseded deletes rows an edit left keyed on its own event ID, now that the
// edit folds onto its target.
func dropSuperseded(ctx context.Context, tx *sql.Tx, roomID domain.RoomID, msgs []domain.Message) error {
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM messages WHERE room_id = ? AND event_id = ?`)
	if err != nil {
		return fmt.Errorf("db: prepare revision cleanup: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range msgs {
		m := &msgs[i]
		if m.RevisionID == "" || m.RevisionID == m.ID {
			continue
		}
		if _, derr := stmt.ExecContext(ctx, string(roomID), string(m.RevisionID)); derr != nil {
			return fmt.Errorf("db: drop superseded row %s: %w", m.RevisionID, derr)
		}
	}
	return nil
}

// LatestEvents returns the newest cached event ID for each room that has one, for
// marking a room read without opening it.
func (c *Cache) LatestEvents(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]domain.EventID, error) {
	latest := make(map[domain.RoomID]domain.EventID, len(roomIDs))
	if len(roomIDs) == 0 {
		return latest, nil
	}
	stmt, err := c.db.PrepareContext(ctx,
		"SELECT event_id FROM messages WHERE room_id = ? ORDER BY ts_ms DESC, event_id DESC LIMIT 1")
	if err != nil {
		return nil, fmt.Errorf("db: prepare latest event: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, roomID := range roomIDs {
		var eventID string
		found, err := optional(stmt.QueryRowContext(ctx, string(roomID)).Scan(&eventID))
		if err != nil {
			return nil, fmt.Errorf("db: latest event of %s: %w", roomID, err)
		}
		if found {
			latest[roomID] = domain.EventID(eventID)
		}
	}
	return latest, nil
}

// NewestTS is the latest time among a room's cached messages and their revisions
// (edits); 0 when nothing is cached. Reactions and redactions carry no time here.
func (c *Cache) NewestTS(ctx context.Context, roomID domain.RoomID) (int64, error) {
	var ts int64
	err := c.db.QueryRowContext(ctx, `
		SELECT max(coalesce((SELECT max(ts_ms) FROM messages WHERE room_id = ?1), 0),
		           coalesce((SELECT max(ts_ms) FROM message_revisions WHERE room_id = ?1), 0))`,
		string(roomID)).Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("db: newest time in %s: %w", roomID, err)
	}
	return ts, nil
}

// ThreadMessages is one thread in order: its root (if cached) first, then up to
// limit of its newest replies.
func (c *Cache) ThreadMessages(
	ctx context.Context, roomID domain.RoomID, root domain.EventID, limit int,
) ([]domain.Message, error) {
	replies, err := collect(ctx, c.db, "thread messages",
		messageSelect+` WHERE m.room_id = ? AND m.thread_root = ? ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?`,
		scanMessage(roomID), string(roomID), string(root), limit)
	if err != nil {
		return nil, err
	}
	slices.Reverse(replies)
	rootMsg, found, err := c.Message(ctx, roomID, root)
	if err != nil {
		return nil, err
	}
	if found {
		return append([]domain.Message{rootMsg}, replies...), nil
	}
	return replies, nil
}

// LatestInThread is the newest cached event in a thread, or "" when none is cached;
// the fallback m.in_reply_to for sending into it.
func (c *Cache) LatestInThread(ctx context.Context, roomID domain.RoomID, root domain.EventID) (domain.EventID, error) {
	var eventID string
	if _, err := optional(c.db.QueryRowContext(ctx,
		"SELECT event_id FROM messages WHERE room_id = ? AND thread_root = ? ORDER BY ts_ms DESC, event_id DESC LIMIT 1",
		string(roomID), string(root)).Scan(&eventID)); err != nil {
		return "", fmt.Errorf("db: latest in thread %s: %w", root, err)
	}
	return domain.EventID(eventID), nil
}

// LastMessages is when each room last had a message (redactions count). Rooms with
// nothing cached are absent.
func (c *Cache) LastMessages(ctx context.Context) (map[domain.RoomID]time.Time, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT room_id, MAX(ts_ms) AS recent FROM messages GROUP BY room_id")
	if err != nil {
		return nil, fmt.Errorf("db: query last messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	latest := make(map[domain.RoomID]time.Time, 128)
	for rows.Next() {
		var (
			roomID string
			recent int64
		)
		if err := rows.Scan(&roomID, &recent); err != nil {
			return nil, fmt.Errorf("db: scan last message: %w", err)
		}
		if recent <= 0 {
			continue // a cached message with no timestamp says nothing about when
		}
		latest[domain.RoomID(roomID)] = time.UnixMilli(recent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate last messages: %w", err)
	}
	return latest, nil
}

// EachMessageBody streams every non-redacted, non-empty body to fn, saying whether
// one of me (this person's IDs) wrote it. It reads a page at a time and calls fn between pages, so the one
// connection is free for sync and the UI while fn works.
func (c *Cache) EachMessageBody(ctx context.Context, me []string, fn func(body string, mine bool)) error {
	type body struct {
		rowid        int64
		sender, text string
	}
	scan := func(rows *sql.Rows) (body, error) {
		var b body
		return b, rows.Scan(&b.rowid, &b.sender, &b.text)
	}
	var after int64
	for {
		page, err := collect(ctx, c.db, "message bodies",
			"SELECT rowid, sender, body FROM messages WHERE rowid > ? AND redacted = 0 AND body <> '' ORDER BY rowid LIMIT ?",
			scan, after, bodyPage)
		if err != nil {
			return err
		}
		for _, b := range page {
			fn(b.text, slices.Contains(me, b.sender))
		}
		if len(page) < bodyPage {
			return nil
		}
		after = page[len(page)-1].rowid
	}
}

// bodyPage is how many bodies EachMessageBody reads per query.
const bodyPage = 2000

// SenderOf is who sent one cached message; an error when it is not cached.
func (c *Cache) SenderOf(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (string, error) {
	var sender string
	if err := c.db.QueryRowContext(ctx,
		"SELECT sender FROM messages WHERE room_id = ? AND event_id = ?",
		string(roomID), string(eventID)).Scan(&sender); err != nil {
		return "", fmt.Errorf("db: sender of %s: %w", eventID, err)
	}
	return sender, nil
}

// Message is one cached message's body and formatting, and whether it is cached.
func (c *Cache) Message(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, bool, error) {
	var body, html string
	err := c.db.QueryRowContext(ctx, `
		SELECT m.body, COALESCE(h.html, '')
		  FROM messages m
		  LEFT JOIN message_html h ON h.room_id = m.room_id AND h.event_id = m.event_id
		 WHERE m.room_id = ? AND m.event_id = ?`,
		string(roomID), string(eventID)).Scan(&body, &html)
	found, err := optional(err)
	if err != nil || !found {
		if err != nil {
			err = fmt.Errorf("db: read message %s: %w", eventID, err)
		}
		return domain.Message{}, false, err
	}
	return domain.Message{ID: eventID, RoomID: roomID, Body: body, Format: richtext.FromMarkup(html)}, true, nil
}

// messageSelect is every message read: the row plus its optional side tables.
const messageSelect = `SELECT m.event_id, m.sender, m.sender_name, m.body, m.ts_ms, m.redacted, m.edited,
	        m.reply_to, m.mentioned, m.thread_root, m.emote,
	        d.kind, d.name, d.mime, d.width, d.height, d.size, h.html, r.by, r.reason, e.ts_ms, e.revision_id
	   FROM messages m
	   LEFT JOIN message_media d ON d.room_id = m.room_id AND d.event_id = m.event_id
	   LEFT JOIN message_html  h ON h.room_id = m.room_id AND h.event_id = m.event_id
	   LEFT JOIN message_redaction r ON r.room_id = m.room_id AND r.event_id = m.event_id
	   LEFT JOIN message_edit e ON e.room_id = m.room_id AND e.event_id = m.event_id`

// scanMessage reads one row of messageSelect into a message for the given room.
func scanMessage(roomID domain.RoomID) func(*sql.Rows) (domain.Message, error) {
	return func(rows *sql.Rows) (domain.Message, error) {
		var (
			eventID, sender, senderName, body, replyTo, threadRoot string
			tsMS                                                   int64
			redacted, edited, mentioned, emote                     bool
			kind, name, mime                                       sql.NullString
			width, height, mediaSize                               sql.NullInt64
			formatted, redactedBy, redactReason                    sql.NullString
			editedMS                                               sql.NullInt64
			revision                                               sql.NullString
		)
		if err := rows.Scan(&eventID, &sender, &senderName, &body, &tsMS, &redacted, &edited,
			&replyTo, &mentioned, &threadRoot, &emote,
			&kind, &name, &mime, &width, &height, &mediaSize, &formatted,
			&redactedBy, &redactReason, &editedMS, &revision); err != nil {
			return domain.Message{}, err
		}
		msg := domain.Message{
			ID:         domain.EventID(eventID),
			RoomID:     roomID,
			Sender:     sender,
			SenderName: senderName,
			Body:       body,
			Timestamp:  time.UnixMilli(tsMS),
			Redacted:   redacted,
			Edited:     edited,
			ReplyTo:    domain.EventID(replyTo),
			Mentioned:  mentioned,
			ThreadRoot: domain.EventID(threadRoot),
			Emote:      emote,
			Format:     richtext.FromMarkup(formatted.String),
			RedactedBy: redactedBy.String, RedactedReason: redactReason.String,
		}
		if editedMS.Valid {
			msg.EditedAt = time.UnixMilli(editedMS.Int64)
		}
		// The edit shown, so the client breaks an edit tie as the cache did.
		msg.RevisionID = domain.EventID(revision.String)
		if kind.Valid && kind.String != "" {
			msg.Media = &domain.Media{
				Type:   domain.MediaType(kind.String),
				Name:   name.String,
				Mime:   mime.String,
				Width:  int(width.Int64),
				Height: int(height.Int64),
				Size:   int(mediaSize.Int64),
			}
		}
		return msg, nil
	}
}

// MessagesAround is one message with before/after neighbors (counts), oldest
// first. Nil when the message is not cached.
func (c *Cache) MessagesAround(
	ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int,
) ([]domain.Message, error) {
	if event == "" {
		return nil, nil
	}
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	var at int64
	found, err := optional(c.db.QueryRowContext(ctx,
		`SELECT ts_ms FROM messages WHERE room_id = ? AND event_id = ?`,
		string(roomID), string(event)).Scan(&at))
	if err != nil {
		return nil, fmt.Errorf("db: find %s: %w", event, err)
	}
	if !found {
		return nil, nil // "not cached" is an answer, not a failure
	}

	older, err := c.messagesBefore(ctx, roomID, at, event, before)
	if err != nil {
		return nil, err
	}
	newer, err := c.messagesFrom(ctx, roomID, at, event, after+1)
	if err != nil {
		return nil, err
	}
	return append(older, newer...), nil
}

// messagesBefore is the n messages before (at, event) in timeline order (time, then
// event ID, as domain.MergeMessages sorts), oldest first. The pair, not the time
// alone: a bridged burst shares one time.
func (c *Cache) messagesBefore(
	ctx context.Context, roomID domain.RoomID, at int64, event domain.EventID, n int,
) ([]domain.Message, error) {
	if n == 0 {
		return nil, nil
	}
	msgs, err := collect(ctx, c.db, "messages before", messageSelect+`
		WHERE m.room_id = ? AND (m.ts_ms, m.event_id) < (?, ?)
		ORDER BY m.ts_ms DESC, m.event_id DESC LIMIT ?`,
		scanMessage(roomID), string(roomID), at, string(event), n)
	slices.Reverse(msgs)
	return msgs, err
}

// messagesFrom is (at, event) and the messages after it, n in all, oldest first.
func (c *Cache) messagesFrom(
	ctx context.Context, roomID domain.RoomID, at int64, event domain.EventID, n int,
) ([]domain.Message, error) {
	return collect(ctx, c.db, "messages from", messageSelect+`
		WHERE m.room_id = ? AND (m.ts_ms, m.event_id) >= (?, ?)
		ORDER BY m.ts_ms ASC, m.event_id ASC LIMIT ?`,
		scanMessage(roomID), string(roomID), at, string(event), n)
}

// MessagesEndingIn is each cached message in owner's rooms whose ID is its room's, a
// "/", and one of tails: the room and the ID, for a network whose deletions name a
// message by a number unique across an account's rooms rather than by its room.
func (c *Cache) MessagesEndingIn(ctx context.Context, owner domain.RoomOwner, tails []string) ([]domain.Message, error) {
	if len(tails) == 0 || owner == "" {
		return nil, nil
	}
	args := []any{string(owner)}
	for _, tail := range tails {
		args = append(args, tail)
	}
	return collect(ctx, c.db, "messages by their ending", `
		SELECT room_id, event_id FROM messages
		 WHERE substr(room_id, 1, length(?1)) = ?1
		   AND substr(event_id, 1, length(room_id) + 1) = room_id || '/'
		   AND substr(event_id, length(room_id) + 2) IN (`+placeholders(len(tails))+`)`,
		func(rows *sql.Rows) (domain.Message, error) {
			var room, event string
			err := rows.Scan(&room, &event)
			return domain.Message{RoomID: domain.RoomID(room), ID: domain.EventID(event)}, err
		}, args...)
}
