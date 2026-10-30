package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room merges into another when its network learns the two are one chat (a WhatsApp
// direct chat known by the person's LID, then by their number). What the cache holds of
// it moves to the room it joins, and room_aliases keeps where it went, so the network
// files a later write naming it there too.

// MergeRoom merges m.From into m.Into in one transaction: its messages (a message held
// in both keeps Into's row) and every row hung off the room, each numbered anew so a
// resuming client reads them under Into; the drafts joined (domain.RebaseDraft: the
// newer whole, the older's words after it); the later read position, per room and per
// thread; the newer verdict per tag, of the room and of m.FromPerson (kept under
// m.IntoPerson); usage counts added up; Into's members, else From's. From is then gone,
// and its alias kept. changed is false when there was nothing to do: no From, and its
// alias kept already.
func (c *Cache) MergeRoom(ctx context.Context, m domain.RoomMerge) (changed bool, err error) {
	if m.From == "" || m.Into == "" || m.From == m.Into {
		return false, nil
	}
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		changed, err = mergeRoom(ctx, tx, m)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// RoomAliases is every room merged into another, and the room it joined.
func (c *Cache) RoomAliases(ctx context.Context) (map[domain.RoomID]domain.RoomID, error) {
	return collectMap(ctx, c.db, "room aliases", `SELECT room_id, into_id FROM room_aliases`,
		func(rows *sql.Rows) (domain.RoomID, domain.RoomID, error) {
			var from, into string
			err := rows.Scan(&from, &into)
			return domain.RoomID(from), domain.RoomID(into), err
		})
}

// filedUnder is the room a write naming room goes to, inside the caller's transaction:
// the one it was merged into, if any.
func filedUnder(ctx context.Context, tx *sql.Tx, room domain.RoomID) (domain.RoomID, error) {
	var into string
	found, err := optional(tx.QueryRowContext(ctx, `SELECT into_id FROM room_aliases WHERE room_id = ?`, string(room)).Scan(&into))
	if err != nil {
		return "", fmt.Errorf("db: read the alias of %s: %w", room, err)
	}
	if found {
		return domain.RoomID(into), nil
	}
	return room, nil
}

// mergedApart are the tables whose rows mergeRoom joins rather than moves (see
// mergeRows); the rest are moved by mergeTables, a row Into holds already kept.
var mergedApart = []string{
	"room_unread", "thread_read", "drafts", "draft_threads", "tag_verdicts", "mention_usage",
	"emoji_usage", "room_members", "room_aliases", "message_touched",
}

// mergeRoom is MergeRoom inside the caller's transaction.
func mergeRoom(ctx context.Context, tx *sql.Tx, m domain.RoomMerge) (bool, error) {
	from, into := string(m.From), string(m.Into)
	// A message's rows move after it, or before: checked once all have.
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return false, fmt.Errorf("db: merge %s: %w", from, err)
	}
	held, err := keepAlias(ctx, tx, from, into)
	if err != nil {
		return false, err
	}
	if err := mergePersonVerdicts(ctx, tx, m.FromPerson, m.IntoPerson); err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE id = ?)`, from).Scan(&exists); err != nil {
		return false, fmt.Errorf("db: merge %s: %w", from, err)
	}
	if !exists {
		return !held, nil
	}
	// Into takes From's row when it has none of its own, or only a placeholder.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rooms (id, name, is_direct, membership, invited_by, heroes)
		SELECT ?, name, is_direct, membership, invited_by, heroes FROM rooms WHERE id = ? AND true
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, is_direct = excluded.is_direct,
			membership = excluded.membership, invited_by = excluded.invited_by, heroes = excluded.heroes
		WHERE rooms.membership = ''`, into, from); err != nil {
		return false, fmt.Errorf("db: merge %s into %s: %w", from, into, err)
	}
	// Every message From holds is numbered anew under Into, one Into held already too.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO message_touched (room_id, event_id) SELECT ?, event_id FROM messages WHERE room_id = ?`,
		into, from); err != nil {
		return false, fmt.Errorf("db: merge %s into %s: %w", from, into, err)
	}
	if err := mergeRows(ctx, tx, m.From, m.Into); err != nil {
		return false, err
	}
	if err := mergeTables(ctx, tx, from, into); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, from); err != nil {
		return false, fmt.Errorf("db: merge %s into %s: %w", from, into, err)
	}
	return true, nil
}

// keepAlias keeps from as merged into into, and every room merged into from as merged
// into into; held is whether that alias was kept already.
func keepAlias(ctx context.Context, tx *sql.Tx, from, into string) (held bool, err error) {
	was, err := filedUnder(ctx, tx, domain.RoomID(from))
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO room_aliases (room_id, into_id) VALUES (?, ?)
		ON CONFLICT(room_id) DO UPDATE SET into_id = excluded.into_id`, from, into); err != nil {
		return false, fmt.Errorf("db: keep the alias of %s: %w", from, err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE room_aliases SET into_id = ? WHERE into_id = ?`, into, from); err != nil {
		return false, fmt.Errorf("db: keep the aliases into %s: %w", from, err)
	}
	return string(was) == into, nil
}

// mergeTables moves from's rows in every table with a room_id but mergedApart's to
// into; a row into holds already is kept, and from's goes. Read from the schema, so a
// table added later moves too.
func mergeTables(ctx context.Context, tx *sql.Tx, from, into string) error {
	tables, err := collect(ctx, tx, "the tables of rooms", `
		SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) c
		 WHERE m.type = 'table' AND c.name = 'room_id' ORDER BY m.name`,
		func(rows *sql.Rows) (string, error) {
			var name string
			err := rows.Scan(&name)
			return name, err
		})
	if err != nil {
		return err
	}
	for _, table := range tables {
		if slices.Contains(mergedApart, table) {
			continue
		}
		name := quoteIdent(table)
		// #nosec G202 -- the name is the schema's own table's, quoted; every value is bound.
		move, drop := "UPDATE OR IGNORE "+name+" SET room_id = ? WHERE room_id = ?", "DELETE FROM "+name+" WHERE room_id = ?"
		if _, err := tx.ExecContext(ctx, move, into, from); err != nil {
			return fmt.Errorf("db: merge %s's %s into %s: %w", from, table, into, err)
		}
		// What into held already: a table that does not hang off the room keeps none.
		if _, err := tx.ExecContext(ctx, drop, from); err != nil {
			return fmt.Errorf("db: merge %s's %s into %s: %w", from, table, into, err)
		}
	}
	return nil
}

// mergeRows joins from's rows of mergedApart's tables into into's; from's own go with
// the room.
func mergeRows(ctx context.Context, tx *sql.Tx, from, into domain.RoomID) error {
	if err := mergeReadPosition(ctx, tx, from, into); err != nil {
		return err
	}
	if err := mergeDrafts(ctx, tx, from, into); err != nil {
		return err
	}
	for _, stmt := range []string{
		// A thread's later position.
		`INSERT INTO thread_read (room_id, root_event, read_event, read_ts_ms)
		 SELECT ?1, root_event, read_event, read_ts_ms FROM thread_read WHERE room_id = ?2 AND true
		 ON CONFLICT(room_id, root_event) DO UPDATE SET read_event = excluded.read_event, read_ts_ms = excluded.read_ts_ms
		 WHERE (excluded.read_ts_ms, excluded.read_event) > (thread_read.read_ts_ms, thread_read.read_event)`,
		// A tag's newer verdict.
		`INSERT INTO tag_verdicts (room_id, tag, rule, filter, at_ms, released)
		 SELECT ?1, tag, rule, filter, at_ms, released FROM tag_verdicts WHERE room_id = ?2 AND true
		 ON CONFLICT(room_id, tag) DO UPDATE SET rule = excluded.rule, filter = excluded.filter,
			at_ms = excluded.at_ms, released = excluded.released
		 WHERE excluded.at_ms > tag_verdicts.at_ms`,
		`INSERT INTO mention_usage (room_id, user_id, count, last_used_ms)
		 SELECT ?1, user_id, count, last_used_ms FROM mention_usage WHERE room_id = ?2 AND true
		 ON CONFLICT(room_id, user_id) DO UPDATE SET count = mention_usage.count + excluded.count,
			last_used_ms = max(mention_usage.last_used_ms, excluded.last_used_ms)`,
		`INSERT INTO emoji_usage (room_id, kind, emoji, count, last_used_ms)
		 SELECT ?1, kind, emoji, count, last_used_ms FROM emoji_usage WHERE room_id = ?2 AND true
		 ON CONFLICT(room_id, kind, emoji) DO UPDATE SET count = emoji_usage.count + excluded.count,
			last_used_ms = max(emoji_usage.last_used_ms, excluded.last_used_ms)`,
		// The person's ID as into knows it, unless into knows none.
		`INSERT INTO room_members (room_id, user_id, display_name)
		 SELECT ?1, user_id, display_name FROM room_members
		  WHERE room_id = ?2 AND NOT EXISTS (SELECT 1 FROM room_members WHERE room_id = ?1)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, string(into), string(from)); err != nil {
			return fmt.Errorf("db: merge %s into %s: %w", from, into, err)
		}
	}
	return nil
}

// mergeReadPosition keeps into's read position or from's, the later; marked unread on
// either stays so. The counts are into's until the room is counted again.
func mergeReadPosition(ctx context.Context, tx *sql.Tx, from, into domain.RoomID) error {
	type position struct {
		event  string
		ts     int64
		marked bool
	}
	read := func(room domain.RoomID) (position, bool, error) {
		var p position
		found, err := optional(tx.QueryRowContext(ctx, `
			SELECT u.read_event, `+readPosition+`, u.marked FROM room_unread u
			  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event
			 WHERE u.room_id = ?`, string(room)).Scan(&p.event, &p.ts, &p.marked))
		if err != nil {
			return position{}, false, fmt.Errorf("db: read the read position of %s: %w", room, err)
		}
		return p, found, nil
	}
	theirs, found, err := read(from)
	if err != nil || !found {
		return err
	}
	mine, held, err := read(into)
	if err != nil {
		return err
	}
	if held && mine.ts >= theirs.ts {
		theirs.event, theirs.ts = mine.event, mine.ts
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_unread (room_id, read_event, read_ts_ms, marked) VALUES (?, ?, ?, ?)
		ON CONFLICT(room_id) DO UPDATE SET read_event = excluded.read_event, read_ts_ms = excluded.read_ts_ms,
			marked = max(room_unread.marked, excluded.marked)`,
		string(into), theirs.event, theirs.ts, theirs.marked); err != nil {
		return fmt.Errorf("db: merge the read position of %s into %s: %w", from, into, err)
	}
	return nil
}

// mergeDrafts keeps from's draft under into, joined with into's when both have one.
func mergeDrafts(ctx context.Context, tx *sql.Tx, from, into domain.RoomID) error {
	theirs, found, err := wholeDraft(ctx, tx, from)
	if err != nil || !found {
		return err
	}
	theirs.RoomID = into
	mine, held, err := wholeDraft(ctx, tx, into)
	if err != nil {
		return err
	}
	joined := theirs
	if held {
		newer, older := mine, theirs
		if theirs.Updated.After(mine.Updated) {
			newer, older = theirs, mine
		}
		joined, _ = domain.RebaseDraft(newer, domain.StoredDraft{}, older, nil)
		for _, mention := range older.Mentions {
			if !slices.Contains(joined.Mentions, mention) {
				joined.Mentions = append(joined.Mentions, mention)
			}
		}
	}
	if joined.Updated.IsZero() {
		joined.Updated = time.Now()
	}
	return writeDraft(ctx, tx, joined)
}

// mergePersonVerdicts keeps each verdict on from (a person's ID) under into, the newer
// where into has one for the tag too, and drops from's.
func mergePersonVerdicts(ctx context.Context, tx *sql.Tx, from, into string) error {
	if from == "" || into == "" || from == into {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO person_verdicts (account, person, tag, rule, filter, at_ms, released)
		SELECT account, ?, tag, rule, filter, at_ms, released FROM person_verdicts WHERE person = ? AND true
		ON CONFLICT(account, person, tag) DO UPDATE SET rule = excluded.rule, filter = excluded.filter,
			at_ms = excluded.at_ms, released = excluded.released
		WHERE excluded.at_ms > person_verdicts.at_ms`, into, from); err != nil {
		return fmt.Errorf("db: merge %s's verdicts into %s: %w", from, into, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE person = ?`, from); err != nil {
		return fmt.Errorf("db: merge %s's verdicts into %s: %w", from, into, err)
	}
	return nil
}
