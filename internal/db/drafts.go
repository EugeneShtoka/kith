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
		var qerr error
		// A window still on a room since merged writes the room it joined.
		if draft.RoomID, qerr = filedUnder(ctx, tx, draft.RoomID); qerr != nil {
			return qerr
		}
		current, found, qerr := wholeDraft(ctx, tx, draft.RoomID, draft.ThreadRoot)
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
		var qerr error
		// A window still on a room since merged writes the room it joined.
		if draft.RoomID, qerr = filedUnder(ctx, tx, draft.RoomID); qerr != nil {
			return qerr
		}
		current, _, qerr := wholeDraft(ctx, tx, draft.RoomID, draft.ThreadRoot)
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

// unixMillisOrZero is t in milliseconds, 0 for the zero time.
func unixMillisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// writeDraft writes a draft inside the caller's transaction: the room's own, or its
// thread's.
func writeDraft(ctx context.Context, tx *sql.Tx, draft domain.StoredDraft) error {
	room, thread := string(draft.RoomID), string(draft.ThreadRoot)
	if draft.Empty() {
		query, args := `DELETE FROM drafts WHERE room_id = ?`, []any{room}
		if thread != "" {
			query, args = `DELETE FROM thread_drafts WHERE room_id = ? AND thread_root = ?`, []any{room, thread}
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
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
	query := `
			INSERT INTO drafts (room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id) DO UPDATE SET
				body = excluded.body, caret = excluded.caret, mentions = excluded.mentions,
				reply_to = excluded.reply_to, editing = excluded.editing,
				edit_saved = excluded.edit_saved, author = excluded.author,
				updated_ms = excluded.updated_ms`
	key := []any{room}
	if thread != "" {
		query = `
			INSERT INTO thread_drafts (room_id, thread_root, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(room_id, thread_root) DO UPDATE SET
				body = excluded.body, caret = excluded.caret, mentions = excluded.mentions,
				reply_to = excluded.reply_to, editing = excluded.editing,
				edit_saved = excluded.edit_saved, author = excluded.author,
				updated_ms = excluded.updated_ms`
		key = append(key, thread)
	}
	if _, err := tx.ExecContext(ctx, query, append(key, draft.Body, draft.Caret, string(mentions),
		string(draft.ReplyTo), string(draft.Editing), draft.EditSaved,
		draft.Author, updated.UnixMilli())...); err != nil {
		return fmt.Errorf("db: save draft for %s: %w", draft.RoomID, err)
	}
	return nil
}

// Drafts is every stored draft, newest first.
func (c *Cache) Drafts(ctx context.Context) ([]domain.StoredDraft, error) {
	return collect(ctx, c.db, "drafts", draftRows+` ORDER BY updated_ms DESC`,
		func(rows *sql.Rows) (domain.StoredDraft, error) {
			draft, mentions, err := scanDraft(rows)
			if err != nil {
				return domain.StoredDraft{}, err
			}
			// Unreadable mentions are dropped, keeping the draft; logged, as it would
			// then send without notifying them.
			if mentions != "" {
				if jerr := json.Unmarshal([]byte(mentions), &draft.Mentions); jerr != nil {
					draft.Mentions = nil
					c.warn(ctx, "draft mentions unreadable; dropped, the draft is kept", jerr, "room", string(draft.RoomID))
				}
			}
			return draft, nil
		})
}

// wholeDraft is the stored draft of room's thread (the room's own for none) with its
// mentions, inside the caller's transaction; found is false when there is none.
// Unreadable mentions are dropped.
func wholeDraft(ctx context.Context, tx *sql.Tx, room domain.RoomID, thread domain.EventID) (draft domain.StoredDraft, found bool, err error) {
	drafts, err := roomDrafts(ctx, tx, room, `AND thread_root = ?`, string(thread))
	if err != nil || len(drafts) == 0 {
		return domain.StoredDraft{}, false, err
	}
	return drafts[0], true, nil
}

// roomDrafts is room's stored drafts, its own and its threads', narrowed by and (args
// its arguments), inside the caller's transaction. Unreadable mentions are dropped.
func roomDrafts(ctx context.Context, tx *sql.Tx, room domain.RoomID, and string, args ...any) ([]domain.StoredDraft, error) {
	return collect(ctx, tx, "a draft", draftRows+` WHERE room_id = ? `+and,
		func(rows *sql.Rows) (domain.StoredDraft, error) {
			read, mentions, serr := scanDraft(rows)
			if serr == nil && mentions != "" && json.Unmarshal([]byte(mentions), &read.Mentions) != nil {
				read.Mentions = nil
			}
			return read, serr
		}, append([]any{string(room)}, args...)...)
}

// draftRows reads whole drafts, the rooms' own and the threads', as scanDraft takes them.
const draftRows = `
		SELECT room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms, thread_root FROM (
			SELECT room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms, '' AS thread_root
			  FROM drafts
			UNION ALL
			SELECT room_id, body, caret, mentions, reply_to, editing, edit_saved, author, updated_ms, thread_root
			  FROM thread_drafts)`

// scanDraft reads a row of draftRows: the draft, and its mentions as stored.
func scanDraft(rows *sql.Rows) (draft domain.StoredDraft, mentions string, err error) {
	var room, replyTo, editing, thread string
	var updated int64
	if err := rows.Scan(&room, &draft.Body, &draft.Caret, &mentions,
		&replyTo, &editing, &draft.EditSaved, &draft.Author, &updated, &thread); err != nil {
		return domain.StoredDraft{}, "", err
	}
	draft.RoomID = domain.RoomID(room)
	draft.ReplyTo, draft.Editing = domain.EventID(replyTo), domain.EventID(editing)
	draft.ThreadRoot = domain.EventID(thread)
	if updated != 0 {
		draft.Updated = time.UnixMilli(updated)
	}
	return draft, mentions, nil
}
