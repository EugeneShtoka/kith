package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Members returns a room's cached members by display name; limit <= 0 is all.
func (c *Cache) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	query := "SELECT user_id, display_name FROM room_members WHERE room_id = ? ORDER BY display_name, user_id"
	args := []any{string(roomID)}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return collect(ctx, c.db, "members", query, func(rows *sql.Rows) (domain.Member, error) {
		var member domain.Member
		err := rows.Scan(&member.UserID, &member.DisplayName)
		return member, err
	}, args...)
}

// SaveMembers replaces a room's cached membership with a /joined_members
// snapshot, so people who left stop being offered.
func (c *Cache) SaveMembers(ctx context.Context, roomID domain.RoomID, members []domain.Member) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM room_members WHERE room_id = ?", string(roomID)); err != nil {
			return fmt.Errorf("db: clear members of %s: %w", roomID, err)
		}
		stmt, err := tx.PrepareContext(ctx,
			"INSERT INTO room_members(room_id, user_id, display_name) VALUES(?, ?, ?)")
		if err != nil {
			return fmt.Errorf("db: prepare member insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for i := range members {
			member := &members[i]
			if member.UserID == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(roomID), member.UserID, member.DisplayName); err != nil {
				return fmt.Errorf("db: insert member %s: %w", member.UserID, err)
			}
		}
		return nil
	})
}

// SaveMember upserts one person's membership from a member event.
func (c *Cache) SaveMember(ctx context.Context, roomID domain.RoomID, member domain.Member) error {
	if member.UserID == "" {
		return nil
	}
	return c.execInRoom(ctx, roomID, "save member "+member.UserID, `
		INSERT INTO room_members(room_id, user_id, display_name) VALUES(?, ?, ?)
		ON CONFLICT(room_id, user_id) DO UPDATE SET display_name = excluded.display_name`,
		string(roomID), member.UserID, member.DisplayName)
}

// RemoveMember drops one person from a room's cached membership (no-op if absent).
func (c *Cache) RemoveMember(ctx context.Context, roomID domain.RoomID, userID string) error {
	if _, err := c.db.ExecContext(ctx,
		"DELETE FROM room_members WHERE room_id = ? AND user_id = ?",
		string(roomID), userID); err != nil {
		return fmt.Errorf("db: remove member %s of %s: %w", userID, roomID, err)
	}
	return nil
}

// RecordMention counts one mention of userID in roomID at ts (ms), for ranking.
func (c *Cache) RecordMention(ctx context.Context, roomID domain.RoomID, userID string, ts int64) error {
	return c.execInRoom(ctx, roomID, "record mention", `
		INSERT INTO mention_usage(room_id, user_id, count, last_used_ms) VALUES(?, ?, 1, ?)
		ON CONFLICT(room_id, user_id) DO UPDATE SET count = count + 1, last_used_ms = max(last_used_ms, excluded.last_used_ms)`,
		string(roomID), userID, ts)
}

// FrequentMentions returns the MXIDs the user mentions most, this room first, then
// everywhere else; count then recency.
func (c *Cache) FrequentMentions(ctx context.Context, roomID domain.RoomID, limit int) ([]string, error) {
	seen := make(map[string]bool, limit)
	out := make([]string, 0, limit)
	for _, scoped := range []bool{true, false} {
		if len(out) >= limit {
			break
		}
		top, err := c.topMentions(ctx, roomID, scoped, limit)
		if err != nil {
			return nil, err
		}
		for _, userID := range top {
			if len(out) >= limit {
				break
			}
			if userID != "" && !seen[userID] {
				seen[userID] = true
				out = append(out, userID)
			}
		}
	}
	return out, nil
}

// topMentions ranks mentioned MXIDs within one room or across all.
func (c *Cache) topMentions(ctx context.Context, roomID domain.RoomID, scoped bool, limit int) ([]string, error) {
	query := "SELECT user_id, sum(count) AS n, max(last_used_ms) AS recent FROM mention_usage"
	args := []any{}
	if scoped {
		query += " WHERE room_id = ?"
		args = append(args, string(roomID))
	}
	query += " GROUP BY user_id ORDER BY n DESC, recent DESC LIMIT ?"
	args = append(args, limit)

	return collect(ctx, c.db, "mention usage", query, func(rows *sql.Rows) (string, error) {
		var (
			userID string
			count  int
			recent int64
		)
		err := rows.Scan(&userID, &count, &recent)
		return userID, err
	}, args...)
}

// RecentSpeakers returns the MXIDs that most recently posted in a room, newest first.
func (c *Cache) RecentSpeakers(ctx context.Context, roomID domain.RoomID, limit int) ([]string, error) {
	return collect(ctx, c.db, "recent speakers", `
		SELECT sender, max(ts_ms) AS recent FROM messages
		WHERE room_id = ? AND sender != ''
		GROUP BY sender ORDER BY recent DESC LIMIT ?`,
		func(rows *sql.Rows) (string, error) {
			var (
				sender string
				recent int64
			)
			err := rows.Scan(&sender, &recent)
			return sender, err
		}, string(roomID), limit)
}

// WideReachingUsers is everyone (but self) cached as a member of at least minRooms
// rooms: the evidence behind domain.Space.Keeper.
func (c *Cache) WideReachingUsers(ctx context.Context, self string, minRooms int) (map[string]bool, error) {
	return collectMap(ctx, c.db, "wide-reaching users", `
		SELECT user_id FROM room_members
		 WHERE user_id != ?
		 GROUP BY user_id
		HAVING COUNT(DISTINCT room_id) >= ?`,
		func(rows *sql.Rows) (string, bool, error) {
			var userID string
			err := rows.Scan(&userID)
			return userID, true, err
		}, self, minRooms)
}

// SearchSenders is everyone who has posted in rooms, most messages first, named from
// their cached membership in those same rooms (unnamed when they have no name there).
// limit <= 0 is no cap.
func (c *Cache) SearchSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]domain.Member, error) {
	if rooms.None() {
		return nil, nil
	}
	senders, err := c.topSenders(ctx, rooms, limit)
	if err != nil {
		return nil, err
	}
	if len(senders) == 0 {
		return nil, nil
	}
	names, err := c.namesOf(ctx, senders, rooms)
	if err != nil {
		return nil, err
	}
	members := make([]domain.Member, 0, len(senders))
	for _, userID := range senders {
		members = append(members, domain.Member{UserID: userID, DisplayName: names[userID]})
	}
	return members, nil
}

// topSenders ranks senders in a set of rooms by message count, then recency.
func (c *Cache) topSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]string, error) {
	query := "SELECT sender, COUNT(*) AS n, MAX(ts_ms) AS recent FROM messages WHERE sender != ''"
	args := make([]any, 0, len(rooms.IDs)+1)
	if !rooms.All {
		var in string
		in, args = inIDs(args, rooms.IDs)
		// #nosec G202 -- inIDs emits only placeholders; every room ID is bound.
		query += " AND room_id" + in
	}
	query += " GROUP BY sender ORDER BY n DESC, recent DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return collect(ctx, c.db, "senders", query, func(rows *sql.Rows) (string, error) {
		var (
			sender string
			count  int
			recent int64
		)
		err := rows.Scan(&sender, &count, &recent)
		return sender, err
	}, args...)
}

// namesOf is a display name for each person, as they are named in rooms. A name from outside the set would answer for rooms the caller
// never asked about (kith-mcp matches its agent's query against these). Among several,
// the name in the first room by ID wins, so the answer is stable. Lists of any length
// bind (inIDs).
func (c *Cache) namesOf(ctx context.Context, userIDs []string, rooms domain.RoomSet) (map[string]string, error) {
	names := make(map[string]string, len(userIDs))
	if len(userIDs) == 0 || rooms.None() {
		return names, nil
	}
	if err := c.readNames(ctx, userIDs, rooms, names); err != nil {
		return nil, err
	}
	return names, nil
}

// readNames adds one batch of people to names, keeping the first name found for each.
func (c *Cache) readNames(ctx context.Context, userIDs []string, rooms domain.RoomSet, names map[string]string) error {
	users, args := inIDs(make([]any, 0, len(userIDs)+len(rooms.IDs)), userIDs)
	// #nosec G202 -- inIDs emits only placeholders; every ID is bound.
	query := "SELECT user_id, display_name FROM room_members WHERE display_name != '' AND user_id" + users
	if !rooms.All {
		var in string
		in, args = inIDs(args, rooms.IDs)
		query += " AND room_id" + in
	}
	rows, err := c.db.QueryContext(ctx, query+" ORDER BY user_id, room_id", args...)
	if err != nil {
		return fmt.Errorf("db: query member names: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var userID, name string
		if err := rows.Scan(&userID, &name); err != nil {
			return fmt.Errorf("db: scan member name: %w", err)
		}
		if names[userID] == "" {
			names[userID] = name
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: iterate member names: %w", err)
	}
	return nil
}

// RoomsWith is every joined room among rooms whose membership includes all of userIDs,
// most recently active first. The rooms restrict the answer inside the query, so LIMIT
// applies after the scope.
func (c *Cache) RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error) {
	if rooms.None() {
		return nil, nil
	}
	// Distinct: the query matches a room holding as many people as were asked for.
	wanted := make([]string, 0, len(userIDs))
	seen := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	people, args := inIDs(make([]any, 0, len(wanted)+2), wanted)
	within := ""
	if !rooms.All {
		var in string
		in, args = inIDs(args, rooms.IDs)
		within = " AND r.id" + in
	}
	// The activity stamp is a correlated one-row seek per room; an aggregate over
	// a messages join scaled with the whole cache.
	// #nosec G202 -- placeholders emits only "?, ?, ?"; every value is bound.
	query := `
		SELECT r.id, r.name, r.is_direct,
		       COALESCE((SELECT x.ts_ms FROM messages x
		                  WHERE x.room_id = r.id ORDER BY x.ts_ms DESC LIMIT 1), 0) AS last_ms
		FROM room_members rm
		JOIN rooms r ON r.id = rm.room_id
		WHERE rm.user_id` + people + ` AND r.membership = 'join'` + within + `
		GROUP BY r.id
		HAVING COUNT(DISTINCT rm.user_id) = ?
		ORDER BY last_ms DESC, r.id
		LIMIT ?`
	args = append(args, len(wanted), limit)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: rooms with %v: %w", userIDs, err)
	}
	return collectRows(rows, "rooms with members", func(rows *sql.Rows) (domain.Room, error) {
		var room domain.Room
		var direct int
		var lastMS int64
		if err := rows.Scan(&room.ID, &room.Name, &direct, &lastMS); err != nil {
			return domain.Room{}, err
		}
		room.IsDirect = direct != 0
		return room, nil
	})
}

// MemberName is the display name one person has in any cached room, or "" when
// unknown.
func (c *Cache) MemberName(ctx context.Context, userID string) (string, error) {
	var name string
	err := c.db.QueryRowContext(ctx,
		`SELECT display_name FROM room_members WHERE user_id = ? AND display_name <> '' LIMIT 1`,
		userID).Scan(&name)
	if _, err = optional(err); err != nil {
		return "", fmt.Errorf("db: member name for %s: %w", userID, err)
	}
	return name, nil
}
