package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// HoldsRooms reports whether any of owner's joined rooms is cached; without one the
// cache has lost the history owner's sync position assumes.
func (c *Cache) HoldsRooms(ctx context.Context, owner domain.RoomOwner) (bool, error) {
	var holds bool
	err := c.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM rooms WHERE membership = ? AND substr(id, 1, ?) = ?)`,
		membershipJoin, len(owner), string(owner)).Scan(&holds)
	if err != nil {
		return false, fmt.Errorf("db: holds rooms: %w", err)
	}
	return holds, nil
}

// Rooms returns the cached joined rooms, sorted for display. Invitations share
// the table (see Invites).
func (c *Cache) Rooms(ctx context.Context) ([]domain.Room, error) {
	rooms, err := c.roomsWith(ctx, membershipJoin)
	if err != nil {
		return nil, err
	}
	domain.SortRooms(rooms)
	return rooms, nil
}

// roomsWith reads the rooms of one membership, unsorted.
func (c *Cache) roomsWith(ctx context.Context, membership string) ([]domain.Room, error) {
	return collect(ctx, c.db, "rooms",
		`SELECT r.id, r.name, r.is_direct, r.invited_by, r.heroes,
		        COALESCE(t.topic, ''), COALESCE(u.replacement, ''), a.room_id IS NOT NULL,
		        f.room_id IS NOT NULL, COALESCE(n.network, ''), m.room_id IS NOT NULL,
		        o.room_id IS NOT NULL, COALESCE(h.handle, ''),
		        (SELECT json_group_array(room_id ORDER BY room_id) FROM room_aliases WHERE into_id = r.id)
		   FROM rooms r
		   LEFT JOIN room_topics   t ON t.room_id = r.id
		   LEFT JOIN room_upgrades u ON u.room_id = r.id
		   LEFT JOIN room_archived a ON a.room_id = r.id
		   LEFT JOIN room_forums   f ON f.room_id = r.id
		   LEFT JOIN room_networks n ON n.room_id = r.id
		   LEFT JOIN room_sms      m ON m.room_id = r.id
		   LEFT JOIN room_read_only o ON o.room_id = r.id
		   LEFT JOIN room_handles  h ON h.room_id = r.id
		  WHERE r.membership = ?`,
		func(rows *sql.Rows) (domain.Room, error) {
			var (
				id, name, invitedBy, heroes, topic, replacement, network, handle, merged string
				isDirect                                                                 int
				archived, forum, sms, readOnly                                           bool
			)
			if err := rows.Scan(&id, &name, &isDirect, &invitedBy, &heroes, &topic, &replacement, &archived, &forum, &network, &sms, &readOnly, &handle, &merged); err != nil {
				return domain.Room{}, err
			}
			room := domain.Room{
				ID:          domain.RoomID(id),
				Name:        name,
				Topic:       topic,
				IsDirect:    isDirect != 0,
				InvitedBy:   invitedBy,
				Membership:  membershipFrom(membership),
				Replacement: domain.RoomID(replacement),
				Archived:    archived,
				Forum:       forum,
				Network:     domain.Protocol(network),
				SMS:         sms,
				ReadOnly:    readOnly,
				Handle:      handle,
			}
			if heroes != "" {
				if err := json.Unmarshal([]byte(heroes), &room.Members); err != nil {
					return domain.Room{}, fmt.Errorf("decode heroes for %s: %w", id, err)
				}
			}
			if err := json.Unmarshal([]byte(merged), &room.Merged); err != nil {
				return domain.Room{}, fmt.Errorf("decode the rooms merged into %s: %w", id, err)
			}
			if len(room.Merged) == 0 {
				room.Merged = nil
			}
			return room, nil
		}, membership)
}

// registerRoom inserts a placeholder row (empty membership) for a room the room
// list has not caught up with; a refresh promotes it. Placeholders are never swept:
// an upgraded room's predecessor holds history the chain walk needs.
func registerRoom(ctx context.Context, exec execer, roomID domain.RoomID) error {
	if _, err := exec.ExecContext(ctx,
		"INSERT INTO rooms(id, membership) VALUES(?, '') ON CONFLICT(id) DO NOTHING",
		string(roomID)); err != nil {
		return fmt.Errorf("db: register room %s: %w", roomID, err)
	}
	return nil
}

// SetArchived keeps which rooms the network archived, registering unknown ones: a
// network may archive a chat before listing it (WhatsApp's first sync).
func (c *Cache) SetArchived(ctx context.Context, rooms map[domain.RoomID]bool) error {
	return c.setRoomFlags(ctx, rooms, "room_archived", "archived")
}

// SetForums keeps which rooms are made of topics.
func (c *Cache) SetForums(ctx context.Context, rooms map[domain.RoomID]bool) error {
	return c.setRoomFlags(ctx, rooms, "room_forums", "forum")
}

// SetSMS keeps which rooms' messages go out as SMS or MMS.
func (c *Cache) SetSMS(ctx context.Context, rooms map[domain.RoomID]bool) error {
	return c.setRoomFlags(ctx, rooms, "room_sms", "sms")
}

// SetHistoryStart keeps which rooms' history the cache holds back to its first message.
func (c *Cache) SetHistoryStart(ctx context.Context, rooms map[domain.RoomID]bool) error {
	return c.setRoomFlags(ctx, rooms, "room_history_start", "history-start")
}

// HistoryStarts is the rooms whose history the cache holds back to its first message.
func (c *Cache) HistoryStarts(ctx context.Context) (map[domain.RoomID]bool, error) {
	rooms, err := collect(ctx, c.db, "history starts", `SELECT room_id FROM room_history_start`,
		func(rows *sql.Rows) (domain.RoomID, error) {
			var id string
			err := rows.Scan(&id)
			return domain.RoomID(id), err
		})
	if err != nil {
		return nil, err
	}
	out := make(map[domain.RoomID]bool, len(rooms))
	for _, id := range rooms {
		out[id] = true
	}
	return out, nil
}

// SetReadOnly keeps which rooms this account may read but not post in.
func (c *Cache) SetReadOnly(ctx context.Context, rooms map[domain.RoomID]bool) error {
	return c.setRoomFlags(ctx, rooms, "room_read_only", "read-only")
}

// SetHandles keeps rooms' public @usernames (lowercase, no "@"); an empty handle
// removes the row. Unlisted rooms are left untouched: no caller sees every network's
// rooms at once.
func (c *Cache) SetHandles(ctx context.Context, handles map[domain.RoomID]string) error {
	if len(handles) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for id, handle := range handles {
			if handle == "" {
				if _, err := tx.ExecContext(ctx, `DELETE FROM room_handles WHERE room_id = ?`, string(id)); err != nil {
					return fmt.Errorf("db: drop the handle of %s: %w", id, err)
				}
				continue
			}
			if err := registerRoom(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO room_handles(room_id, handle) VALUES(?, ?)
				 ON CONFLICT(room_id) DO UPDATE SET handle=excluded.handle`,
				string(id), handle); err != nil {
				return fmt.Errorf("db: keep the handle of %s: %w", id, err)
			}
		}
		return nil
	})
}

// SetTimers keeps chats' disappearing-messages timers: zero removes a chat's.
func (c *Cache) SetTimers(ctx context.Context, timers map[domain.RoomID]time.Duration) error {
	if len(timers) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for id, timer := range timers {
			if timer <= 0 {
				if _, err := tx.ExecContext(ctx, `DELETE FROM room_timers WHERE room_id = ?`, string(id)); err != nil {
					return fmt.Errorf("db: drop the timer of %s: %w", id, err)
				}
				continue
			}
			if err := registerRoom(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO room_timers(room_id, seconds) VALUES(?, ?)
				ON CONFLICT(room_id) DO UPDATE SET seconds = excluded.seconds`, string(id), int64(timer/time.Second)); err != nil {
				return fmt.Errorf("db: keep the timer of %s: %w", id, err)
			}
		}
		return nil
	})
}

// KeepTimer keeps a chat's timer as something set at version says it: Signal numbers a
// one-to-one chat's timer changes, so one older than the version kept is not taken (an
// equal one is: it says the same change again, or a later message's view of it). Zero
// removes the timer, and version 0 is none. kept says whether it was taken.
func (c *Cache) KeepTimer(ctx context.Context, room domain.RoomID, timer time.Duration, version uint32) (bool, error) {
	kept := false
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		var have int64
		row := tx.QueryRowContext(ctx, `SELECT version FROM room_timer_versions WHERE room_id = ?`, string(room))
		if err := row.Scan(&have); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: read the timer version of %s: %w", room, err)
		}
		if int64(version) < have {
			return nil
		}
		kept = true
		return writeTimer(ctx, tx, room, timer, version)
	})
	return kept, err
}

// writeTimer writes a chat's timer (zero removes it) and, when numbered, its version.
func writeTimer(ctx context.Context, tx *sql.Tx, room domain.RoomID, timer time.Duration, version uint32) error {
	if timer <= 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM room_timers WHERE room_id = ?`, string(room)); err != nil {
			return fmt.Errorf("db: drop the timer of %s: %w", room, err)
		}
	}
	if timer <= 0 && version == 0 {
		return nil
	}
	if err := registerRoom(ctx, tx, room); err != nil {
		return err
	}
	if timer > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_timers(room_id, seconds) VALUES(?, ?)
			ON CONFLICT(room_id) DO UPDATE SET seconds = excluded.seconds`, string(room), int64(timer/time.Second)); err != nil {
			return fmt.Errorf("db: keep the timer of %s: %w", room, err)
		}
	}
	if version > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_timer_versions(room_id, version) VALUES(?, ?)
			ON CONFLICT(room_id) DO UPDATE SET version = excluded.version`, string(room), int64(version)); err != nil {
			return fmt.Errorf("db: keep the timer version of %s: %w", room, err)
		}
	}
	return nil
}

// TimerVersion is a chat's timer and the version it was set at; zeros when none.
func (c *Cache) TimerVersion(ctx context.Context, room domain.RoomID) (time.Duration, uint32, error) {
	timer, err := c.Timer(ctx, room)
	if err != nil {
		return 0, 0, err
	}
	var version int64
	err = c.db.QueryRowContext(ctx, `SELECT version FROM room_timer_versions WHERE room_id = ?`, string(room)).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("db: read the timer version of %s: %w", room, err)
	}
	return timer, uint32(version), nil //nolint:gosec // kept from a uint32
}

// Timer is a chat's disappearing-messages timer; zero when it has none.
func (c *Cache) Timer(ctx context.Context, room domain.RoomID) (time.Duration, error) {
	var seconds int64
	err := c.db.QueryRowContext(ctx, `SELECT seconds FROM room_timers WHERE room_id = ?`, string(room)).Scan(&seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("db: read the timer of %s: %w", room, err)
	}
	return time.Duration(seconds) * time.Second, nil
}

// setRoomFlags keeps rooms' membership of table, a set of room IDs: true adds a room
// (registering it if unknown), false removes it.
func (c *Cache) setRoomFlags(ctx context.Context, rooms map[domain.RoomID]bool, table, flag string) error {
	if len(rooms) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for id, on := range rooms {
			// #nosec G202 -- table is one of the fixed names above; the room is bound.
			query := `DELETE FROM ` + table + ` WHERE room_id = ?`
			if on {
				if err := registerRoom(ctx, tx, id); err != nil {
					return err
				}
				query = `INSERT INTO ` + table + `(room_id) VALUES(?) ON CONFLICT(room_id) DO NOTHING`
			}
			if _, err := tx.ExecContext(ctx, query, string(id)); err != nil {
				return fmt.Errorf("db: keep %s %s=%v: %w", id, flag, on, err)
			}
		}
		return nil
	})
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Stored rooms.membership values; empty is a placeholder (registerRoom). Spelled
// out because domain.MembershipJoin is the empty string.
const (
	membershipJoin   = "join"
	membershipInvite = "invite"
)

// membershipFrom maps the stored word back; anything but an invite is joined.
func membershipFrom(membership string) domain.Membership {
	if membership == membershipInvite {
		return domain.MembershipInvite
	}
	return domain.MembershipJoin
}

// SaveRooms upserts owner's joined rooms and deletes owner's rooms that are gone,
// leaving other networks' rooms alone. An empty snapshot sweeps nothing; a room
// owner does not own is refused.
func (c *Cache) SaveRooms(ctx context.Context, owner domain.RoomOwner, rooms []domain.Room) error {
	for i := range rooms {
		if !owner.Owns(rooms[i].ID) {
			return fmt.Errorf("db: %s is not among %q's rooms", rooms[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertRooms(ctx, tx, rooms, membershipJoin); err != nil {
			return err
		}
		return sweepOwnedRooms(ctx, tx, owner, membershipJoin, rooms)
	})
}

// AddRooms upserts some of owner's joined rooms without sweeping, for rooms learned
// one at a time rather than from a full listing.
func (c *Cache) AddRooms(ctx context.Context, owner domain.RoomOwner, rooms []domain.Room) error {
	for i := range rooms {
		if !owner.Owns(rooms[i].ID) {
			return fmt.Errorf("db: %s is not among %q's rooms", rooms[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error { return upsertRooms(ctx, tx, rooms, membershipJoin) })
}

// JoinRooms marks owner's new or placeholder rooms joined, leaving already joined
// ones untouched, and reports how many changed.
func (c *Cache) JoinRooms(ctx context.Context, owner domain.RoomOwner, ids []domain.RoomID) (int, error) {
	joined := 0
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if !owner.Owns(id) {
				return fmt.Errorf("db: %s is not among %q's rooms", id, owner)
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO rooms(id, membership) VALUES(?, ?)
				ON CONFLICT(id) DO UPDATE SET membership = excluded.membership WHERE rooms.membership = ''`,
				string(id), membershipJoin)
			if err != nil {
				return fmt.Errorf("db: join room %s: %w", id, err)
			}
			if n, err := res.RowsAffected(); err == nil {
				joined += int(n)
			}
		}
		return nil
	})
	return joined, err
}

// sweepOwnedRooms deletes owner's rooms of membership missing from the snapshot. An
// owner is an ID prefix ("whatsapp:<account>/" for an account).
func sweepOwnedRooms(ctx context.Context, tx *sql.Tx, owner domain.RoomOwner, membership string, rooms []domain.Room) error {
	if len(rooms) == 0 {
		return nil
	}
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	in, args := inIDs([]any{membership, len(owner), string(owner)}, ids)
	// #nosec G202 -- inIDs emits only placeholders or a bound json_each.
	query := "DELETE FROM rooms WHERE membership = ? AND substr(id, 1, ?) = ? AND id NOT" + in
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("db: sweep %q's rooms: %w", owner, err)
	}
	return nil
}

// upsertRooms writes each room, leaving whatever hangs off it in place.
func upsertRooms(ctx context.Context, tx *sql.Tx, rooms []domain.Room, membership string) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rooms(id, name, is_direct, membership, invited_by, heroes) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			is_direct=excluded.is_direct,
			membership=excluded.membership,
			invited_by=excluded.invited_by,
			heroes=excluded.heroes`)
	if err != nil {
		return fmt.Errorf("db: prepare room upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range rooms {
		room := &rooms[i]
		var heroes string
		if len(room.Members) > 0 {
			b, err := json.Marshal(room.Members)
			if err != nil {
				return fmt.Errorf("db: encode heroes for %s: %w", room.ID, err)
			}
			heroes = string(b)
		}
		if _, err := stmt.ExecContext(ctx, string(room.ID), room.Name, boolToInt(room.IsDirect),
			membership, room.InvitedBy, heroes); err != nil {
			return fmt.Errorf("db: upsert room %s: %w", room.ID, err)
		}
	}
	return upsertTopics(ctx, tx, rooms)
}

// upsertTopics writes each room's topic, deleting the row when it has none.
func upsertTopics(ctx context.Context, tx *sql.Tx, rooms []domain.Room) error {
	set, err := tx.PrepareContext(ctx, `
		INSERT INTO room_topics(room_id, topic) VALUES(?, ?)
		ON CONFLICT(room_id) DO UPDATE SET topic=excluded.topic`)
	if err != nil {
		return fmt.Errorf("db: prepare topic upsert: %w", err)
	}
	defer func() { _ = set.Close() }()
	clear, err := tx.PrepareContext(ctx, `DELETE FROM room_topics WHERE room_id = ?`)
	if err != nil {
		return fmt.Errorf("db: prepare topic delete: %w", err)
	}
	defer func() { _ = clear.Close() }()
	for i := range rooms {
		room := &rooms[i]
		stmt, args := clear, []any{string(room.ID)}
		if room.Topic != "" {
			stmt, args = set, []any{string(room.ID), room.Topic}
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("db: write topic for %s: %w", room.ID, err)
		}
	}
	return nil
}

// Spaces returns the cached spaces with their child rooms, sorted for display.
func (c *Cache) Spaces(ctx context.Context) ([]domain.Space, error) {
	spaces, err := collect(ctx, c.db, "spaces",
		`SELECT s.id, s.name, s.bridge, s.keeper, coalesce(l.leaving, 0)
		 FROM spaces s LEFT JOIN space_leaving l ON l.space_id = s.id`,
		func(rows *sql.Rows) (domain.Space, error) {
			var id, name, bridge, keeper string
			var leaving int
			err := rows.Scan(&id, &name, &bridge, &keeper, &leaving)
			return domain.Space{
				ID:      domain.SpaceID(id),
				Name:    name,
				Bridge:  domain.Protocol(bridge),
				Keeper:  keeper,
				Leaving: domain.SpaceLeaving(leaving),
			}, err
		})
	if err != nil {
		return nil, err
	}
	index := make(map[domain.SpaceID]int, len(spaces))
	for i := range spaces {
		index[spaces[i].ID] = i
	}
	if childErr := c.loadChildren(ctx, spaces, index); childErr != nil {
		return nil, childErr
	}
	origins, originErr := c.OriginSpaces(ctx)
	if originErr != nil {
		return nil, originErr
	}
	for i := range spaces {
		spaces[i].Original = origins[spaces[i].ID]
	}
	domain.SortSpaces(spaces)
	return spaces, nil
}

// loadChildren attaches each space's child room IDs in position order.
func (c *Cache) loadChildren(ctx context.Context, spaces []domain.Space, index map[domain.SpaceID]int) error {
	children, err := collect(ctx, c.db, "space children",
		"SELECT space_id, room_id FROM space_children ORDER BY space_id, position",
		func(rows *sql.Rows) (spaceChild, error) {
			var ch spaceChild
			err := rows.Scan(&ch.space, &ch.room)
			return ch, err
		})
	if err != nil {
		return err
	}
	for _, ch := range children {
		if i, ok := index[domain.SpaceID(ch.space)]; ok {
			spaces[i].Children = append(spaces[i].Children, domain.RoomID(ch.room))
		}
	}
	return nil
}

type spaceChild struct{ space, room string }

// SaveSpaces replaces owner's cached spaces and their children, leaving others'
// alone. A space owner does not own is refused.
func (c *Cache) SaveSpaces(ctx context.Context, owner domain.RoomOwner, spaces []domain.Space) error {
	for i := range spaces {
		if !owner.Owns(domain.RoomID(spaces[i].ID)) {
			return fmt.Errorf("db: space %s is not among %q's", spaces[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		// Children cascade; owner is bound as an ID prefix.
		if _, err := tx.ExecContext(ctx, "DELETE FROM spaces WHERE substr(id, 1, ?) = ?", len(owner), string(owner)); err != nil {
			return fmt.Errorf("db: clear %q's spaces: %w", owner, err)
		}
		space, err := tx.PrepareContext(ctx, "INSERT INTO spaces(id, name, bridge, keeper) VALUES(?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("db: prepare space insert: %w", err)
		}
		defer func() { _ = space.Close() }()
		child, err := tx.PrepareContext(ctx, "INSERT INTO space_children(space_id, room_id, position) VALUES(?, ?, ?)")
		if err != nil {
			return fmt.Errorf("db: prepare space-child insert: %w", err)
		}
		defer func() { _ = child.Close() }()
		for i := range spaces {
			if err := saveSpace(ctx, tx, space, child, spaces[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// saveSpace writes one space, how it is left, and its children.
func saveSpace(ctx context.Context, tx *sql.Tx, space, child *sql.Stmt, s domain.Space) error {
	if _, err := space.ExecContext(ctx, string(s.ID), s.Name, string(s.Bridge), s.Keeper); err != nil {
		return fmt.Errorf("db: insert space %s: %w", s.ID, err)
	}
	if s.Leavable() {
		if _, err := tx.ExecContext(ctx, "INSERT INTO space_leaving(space_id, leaving) VALUES(?, ?)", string(s.ID), int(s.Leaving)); err != nil {
			return fmt.Errorf("db: keep how space %s is left: %w", s.ID, err)
		}
	}
	for pos, roomID := range s.Children {
		if _, err := child.ExecContext(ctx, string(s.ID), string(roomID), pos); err != nil {
			return fmt.Errorf("db: insert space child %s/%s: %w", s.ID, roomID, err)
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
