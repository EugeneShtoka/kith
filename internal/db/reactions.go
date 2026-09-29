package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reactions returns every cached reaction for a room.
func (c *Cache) Reactions(ctx context.Context, roomID domain.RoomID) ([]domain.Reaction, error) {
	return collect(ctx, c.db, "reactions",
		"SELECT event_id, target_event, sender, emoji FROM reactions WHERE room_id = ?",
		func(rows *sql.Rows) (domain.Reaction, error) {
			var eventID, target, sender, emoji string
			err := rows.Scan(&eventID, &target, &sender, &emoji)
			return domain.Reaction{
				ID:     domain.EventID(eventID),
				RoomID: roomID,
				Target: domain.EventID(target),
				Sender: sender,
				Key:    emoji,
			}, err
		}, string(roomID))
}

// SaveReactions upserts reactions keyed by their own event ID, skipping ones without.
func (c *Cache) SaveReactions(ctx context.Context, rs []domain.Reaction) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for i := range rs {
			if err := registerRoom(ctx, tx, rs[i].RoomID); err != nil {
				return err
			}
		}
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO reactions(event_id, room_id, target_event, sender, emoji) VALUES(?, ?, ?, ?, ?)
			ON CONFLICT(event_id) DO UPDATE SET room_id=excluded.room_id, target_event=excluded.target_event, sender=excluded.sender, emoji=excluded.emoji`)
		if err != nil {
			return fmt.Errorf("db: prepare reaction upsert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, r := range rs {
			if r.ID == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(r.ID), string(r.RoomID), string(r.Target), r.Sender, r.Key); err != nil {
				return fmt.Errorf("db: upsert reaction %s: %w", r.ID, err)
			}
		}
		return nil
	})
}

// RecordEmoji bumps one emoji's usage count in a room and vocabulary, stamping ts.
func (c *Cache) RecordEmoji(ctx context.Context, kind domain.EmojiKind, roomID domain.RoomID, emoji string, ts int64) error {
	return c.execInRoom(ctx, roomID, "record "+string(kind)+" emoji", `
		INSERT INTO emoji_usage(room_id, kind, emoji, count, last_used_ms) VALUES(?, ?, ?, 1, ?)
		ON CONFLICT(room_id, kind, emoji) DO UPDATE SET count = count + 1, last_used_ms = max(last_used_ms, excluded.last_used_ms)`,
		string(roomID), string(kind), emoji, ts)
}

// DeleteReaction removes a reaction (an un-react) and returns it atomically via
// RETURNING. ok is false when it was not cached, i.e. the redaction targets a message.
func (c *Cache) DeleteReaction(ctx context.Context, eventID domain.EventID) (domain.Reaction, bool, error) {
	var target, room, sender, emoji string
	found, err := optional(c.db.QueryRowContext(ctx,
		"DELETE FROM reactions WHERE event_id = ? RETURNING room_id, target_event, sender, emoji",
		string(eventID)).Scan(&room, &target, &sender, &emoji))
	if !found {
		if err != nil {
			err = fmt.Errorf("db: delete reaction %s: %w", eventID, err)
		}
		return domain.Reaction{}, false, err
	}
	return domain.Reaction{
		ID:     eventID,
		RoomID: domain.RoomID(room),
		Target: domain.EventID(target),
		Sender: sender,
		Key:    emoji,
	}, true, nil
}

// Emoji score weights: overlapping terms, so a use here scores 10+3+1, in a
// sibling room 3+1, elsewhere 1. Scope "space" drops the room term, "global" both.
const (
	roomWeight  = 10
	spaceWeight = 3
)

// Emoji scopes as spelled in [display.reactions]; "static" never reaches here.
const (
	scopeSpace  = "space"
	scopeGlobal = "global"
)

// emojiScoreQuery is the weighted score query for one kind. Reactions are counted
// from this account's own annotations (idempotent across re-paging); composed
// emoji have no such record, so they use the emoji_usage tally.
func emojiScoreQuery(kind domain.EmojiKind, roomID domain.RoomID, spaceRooms []domain.RoomID, me, scope string) (string, []any) {
	// Terms a scope excludes are left out of the SQL so args match.
	var terms []string
	var args []any
	if scope != scopeSpace && scope != scopeGlobal {
		terms = append(terms, fmt.Sprintf("SUM(CASE WHEN room_id = ? THEN %%s ELSE 0 END) * %d", roomWeight))
		args = append(args, string(roomID))
	}
	if scope != scopeGlobal && len(spaceRooms) > 0 {
		var in string
		in, args = inIDs(args, spaceRooms)
		terms = append(terms, fmt.Sprintf("SUM(CASE WHEN room_id%s THEN %%s ELSE 0 END) * %d", in, spaceWeight))
	}

	if kind == domain.EmojiReaction {
		if me == "" {
			return "", nil
		}
		return sumOver(terms, "1", "COUNT(*)",
			"FROM reactions WHERE sender = ? AND emoji <> '' GROUP BY emoji"), append(args, me)
	}
	return sumOver(terms, "count", "SUM(count)",
		"FROM emoji_usage WHERE kind = ? GROUP BY emoji"), append(args, string(kind))
}

// sumOver assembles the score query: the scope's terms over per-row value each,
// plus the global term all.
func sumOver(terms []string, each, all, from string) string {
	sum := make([]string, 0, len(terms)+1)
	for _, t := range terms {
		sum = append(sum, fmt.Sprintf(t, each))
	}
	sum = append(sum, all)
	return "SELECT emoji, " + strings.Join(sum, " + ") + " AS score " + from
}

// EmojiScores is every used emoji of one kind with its score for a room (see the
// weights above), in one query. Unused emoji are absent.
func (c *Cache) EmojiScores(
	ctx context.Context,
	kind domain.EmojiKind,
	roomID domain.RoomID,
	spaceRooms []domain.RoomID,
	me string,
	scope string,
) (map[string]int, error) {
	query, args := emojiScoreQuery(kind, roomID, spaceRooms, me, scope)
	if query == "" {
		return map[string]int{}, nil
	}

	type score struct {
		emoji string
		n     int
	}
	all, err := collect(ctx, c.db, string(kind)+" emoji scores", query,
		func(rows *sql.Rows) (score, error) {
			var s score
			err := rows.Scan(&s.emoji, &s.n)
			return s, err
		}, args...)
	if err != nil {
		return nil, err
	}
	scores := map[string]int{}
	for _, s := range all {
		if s.emoji != "" && s.n > 0 {
			scores[s.emoji] = s.n
		}
	}
	return scores, nil
}
