package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ReplaceDraft replaces (or, when empty, deletes) a room's draft only while the stored
// one still equals over, composition and time; a zero over means none stored. Writers
// can share a millisecond, so the composition is compared too. saved is false when
// another writer got there first.
func (c *Cache) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (saved bool, err error) {
	if draft.RoomID == "" {
		return false, nil
	}
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		current, found, qerr := readDraft(ctx, tx, draft.RoomID)
		if qerr != nil {
			return qerr
		}
		// Absent matches only a writer that read none, or a deleted draft comes back.
		unchanged := !found && over.Body == "" && over.Updated.IsZero()
		if found {
			unchanged = current.Body == over.Body && current.ReplyTo == over.ReplyTo && current.Editing == over.Editing &&
				current.EditSaved == over.EditSaved && current.ThreadRoot == over.ThreadRoot &&
				unixMillisOrZero(current.Updated) == unixMillisOrZero(over.Updated)
		}
		if !unchanged {
			return nil
		}
		saved = true
		return writeDraft(ctx, tx, draft)
	})
	return saved, err
}

// RebaseDraft stores a window's draft, written over base (the version it last read;
// zero: none), in one step with reading what is stored: words another writer added
// since go after the window's (domain.RebaseDraft), and unanswered are the window's
// writes it never heard back about. stored is the draft now stored (empty: none), added
// the words of another writer's it took in.
func (c *Cache) RebaseDraft(ctx context.Context, draft, base domain.StoredDraft, unanswered []domain.StoredDraft) (stored domain.StoredDraft, added string, err error) {
	if draft.RoomID == "" {
		return domain.StoredDraft{}, "", nil
	}
	if draft.Updated.IsZero() {
		draft.Updated = time.Now()
	}
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		current, _, qerr := readDraft(ctx, tx, draft.RoomID)
		if qerr != nil {
			return qerr
		}
		stored, added = domain.RebaseDraft(draft, base, current, unanswered)
		return writeDraft(ctx, tx, stored)
	})
	if err != nil {
		return domain.StoredDraft{}, "", err
	}
	return stored, added, nil
}

// readDraft is room's stored draft, its composition and time, inside the caller's
// transaction; found is false when there is none.
func readDraft(ctx context.Context, tx *sql.Tx, room domain.RoomID) (draft domain.StoredDraft, found bool, err error) {
	var replyTo, editing, thread string
	var updated int64
	found, err = optional(tx.QueryRowContext(ctx, `
		SELECT d.body, d.reply_to, d.editing, d.edit_saved, d.updated_ms, coalesce(t.thread_root, '')
		  FROM drafts d LEFT JOIN draft_threads t ON t.room_id = d.room_id
		 WHERE d.room_id = ?`,
		string(room)).Scan(&draft.Body, &replyTo, &editing, &draft.EditSaved, &updated, &thread))
	if err != nil {
		return domain.StoredDraft{}, false, fmt.Errorf("db: read draft for %s: %w", room, err)
	}
	if !found {
		return domain.StoredDraft{}, false, nil
	}
	draft.RoomID = room
	draft.ReplyTo, draft.Editing, draft.ThreadRoot = domain.EventID(replyTo), domain.EventID(editing), domain.EventID(thread)
	if updated != 0 {
		draft.Updated = time.UnixMilli(updated)
	}
	return draft, true, nil
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
	if draft.ThreadRoot == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM draft_threads WHERE room_id = ?`, string(draft.RoomID)); err != nil {
			return fmt.Errorf("db: save draft thread for %s: %w", draft.RoomID, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
			INSERT INTO draft_threads (room_id, thread_root) VALUES (?, ?)
			ON CONFLICT(room_id) DO UPDATE SET thread_root = excluded.thread_root`,
		string(draft.RoomID), string(draft.ThreadRoot)); err != nil {
		return fmt.Errorf("db: save draft thread for %s: %w", draft.RoomID, err)
	}
	return nil
}

// Drafts is every stored draft, newest first.
func (c *Cache) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return collect(ctx, c.db, "drafts", `
		SELECT d.room_id, d.body, d.caret, d.mentions, d.reply_to, d.editing, d.edit_saved, d.author,
		       d.updated_ms, coalesce(t.thread_root, '')
		  FROM drafts d LEFT JOIN draft_threads t ON t.room_id = d.room_id
		 ORDER BY d.updated_ms DESC`,
		func(rows *sql.Rows) (domain.StoredDraft, error) {
			var draft domain.StoredDraft
			var room, mentions, replyTo, editing, thread string
			var updated int64
			if err := rows.Scan(&room, &draft.Body, &draft.Caret, &mentions,
				&replyTo, &editing, &draft.EditSaved, &draft.Author, &updated, &thread); err != nil {
				return domain.StoredDraft{}, err
			}
			draft.RoomID = domain.RoomID(room)
			draft.ReplyTo, draft.Editing = domain.EventID(replyTo), domain.EventID(editing)
			draft.ThreadRoot = domain.EventID(thread)
			if updated != 0 {
				draft.Updated = time.UnixMilli(updated)
			}
			// Unreadable mentions are dropped, keeping the draft; logged, as it would
			// then send without notifying them.
			if mentions != "" {
				if jerr := json.Unmarshal([]byte(mentions), &draft.Mentions); jerr != nil {
					draft.Mentions = nil
					c.warn(ctx, "draft mentions unreadable; dropped, the draft is kept", jerr, "room", room)
				}
			}
			return draft, nil
		})
}
