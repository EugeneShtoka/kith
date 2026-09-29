package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ReplaceDraft writes one room's draft, replacing it wholesale (or deleting it when
// empty), only while the stored draft is still over, as its writer read it: the same
// composition (words, reply and edit targets, the draft an edit returns to) and time;
// a zero over means none is stored. The time is the writer's clock, so two writers can
// stamp one millisecond: the composition decides then. saved is false when another
// writer got there first: a write built on what was read would otherwise drop what
// they wrote.
func (c *Cache) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (saved bool, err error) {
	if draft.RoomID == "" {
		return false, nil
	}
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		var body, replyTo, editing, editSaved string
		var updated int64
		found, qerr := optional(tx.QueryRowContext(ctx,
			"SELECT body, reply_to, editing, edit_saved, updated_ms FROM drafts WHERE room_id = ?",
			string(draft.RoomID)).Scan(&body, &replyTo, &editing, &editSaved, &updated))
		if qerr != nil {
			return fmt.Errorf("db: read draft for %s: %w", draft.RoomID, qerr)
		}
		// Absent matches only a writer that read none: one that read a draft, since
		// deleted, would bring it back.
		unchanged := !found && over.Body == "" && over.Updated.IsZero()
		if found {
			unchanged = body == over.Body && replyTo == string(over.ReplyTo) && editing == string(over.Editing) &&
				editSaved == over.EditSaved && updated == unixMillisOrZero(over.Updated)
		}
		if !unchanged {
			return nil
		}
		saved = true
		return writeDraft(ctx, tx, draft)
	})
	return saved, err
}

// unixMillisOrZero is t in milliseconds, 0 for the zero time.
func unixMillisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// writeDraft writes a draft inside the caller's transaction.
func writeDraft(ctx context.Context, tx *sql.Tx, draft domain.StoredDraft) error {
	if draft.Empty() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM drafts WHERE room_id = ?`, string(draft.RoomID)); err != nil {
			return fmt.Errorf("db: delete draft for %s: %w", draft.RoomID, err)
		}
		return nil
	}
	mentions, err := json.Marshal(draft.Mentions)
	if err != nil {
		return fmt.Errorf("db: encode draft mentions: %w", err)
	}
	updated := draft.Updated
	if updated.IsZero() {
		updated = time.Now()
	}
	if err := registerRoom(ctx, tx, draft.RoomID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
			INSERT INTO drafts (room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id) DO UPDATE SET
				body = excluded.body, caret = excluded.caret, mentions = excluded.mentions,
				reply_to = excluded.reply_to, editing = excluded.editing,
				edit_saved = excluded.edit_saved, author = excluded.author,
				updated_ms = excluded.updated_ms`,
		string(draft.RoomID), draft.Body, draft.Caret, string(mentions),
		string(draft.ReplyTo), string(draft.Editing), draft.EditSaved,
		draft.Author, updated.UnixMilli()); err != nil {
		return fmt.Errorf("db: save draft for %s: %w", draft.RoomID, err)
	}
	return nil
}

// Drafts is every stored draft, newest first.
func (c *Cache) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return collect(ctx, c.db, "drafts", `
		SELECT room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms
		FROM drafts ORDER BY updated_ms DESC`,
		func(rows *sql.Rows) (domain.StoredDraft, error) {
			var draft domain.StoredDraft
			var room, mentions, replyTo, editing string
			var updated int64
			if err := rows.Scan(&room, &draft.Body, &draft.Caret, &mentions,
				&replyTo, &editing, &draft.EditSaved, &draft.Author, &updated); err != nil {
				return domain.StoredDraft{}, err
			}
			draft.RoomID = domain.RoomID(room)
			draft.ReplyTo, draft.Editing = domain.EventID(replyTo), domain.EventID(editing)
			if updated != 0 {
				draft.Updated = time.UnixMilli(updated)
			}
			// Unreadable mentions are dropped rather than losing the draft — logged,
			// since the draft would then send without notifying who it named.
			if mentions != "" {
				if jerr := json.Unmarshal([]byte(mentions), &draft.Mentions); jerr != nil {
					draft.Mentions = nil
					c.warn(ctx, "draft mentions unreadable; dropped, the draft is kept", jerr, "room", room)
				}
			}
			return draft, nil
		})
}
