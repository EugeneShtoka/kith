package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// HoldsRooms reports whether any joined room is cached. An empty cache has lost
// whatever history the sync position assumes it holds.
func (c *Cache) HoldsRooms(ctx context.Context) (bool, error) {
	var holds bool
	err := c.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM rooms WHERE membership = ?)`, membershipJoin).Scan(&holds)
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
		        f.room_id IS NOT NULL
		   FROM rooms r
		   LEFT JOIN room_topics   t ON t.room_id = r.id
		   LEFT JOIN room_upgrades u ON u.room_id = r.id
		   LEFT JOIN room_archived a ON a.room_id = r.id
		   LEFT JOIN room_forums   f ON f.room_id = r.id
		  WHERE r.membership = ?`,
		func(rows *sql.Rows) (domain.Room, error) {
			var (
				id, name, invitedBy, heroes, topic, replacement string
				isDirect                                        int
				archived, forum                                 bool
			)
			if err := rows.Scan(&id, &name, &isDirect, &invitedBy, &heroes, &topic, &replacement, &archived, &forum); err != nil {
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
			}
			if heroes != "" {
				if err := json.Unmarshal([]byte(heroes), &room.Members); err != nil {
					return domain.Room{}, fmt.Errorf("decode heroes for %s: %w", id, err)
				}
			}
			return room, nil
		}, membership)
}

// registerRoom inserts a placeholder row (empty membership) so the sync stream can
// write about a room the room list has not caught up with; the next refresh
// promotes it. Unpromoted rows are deliberately never swept: a registered
// predecessor of an upgraded room holds history the chain walk needs.
func registerRoom(ctx context.Context, exec execer, roomID domain.RoomID) error {
	if _, err := exec.ExecContext(ctx,
		"INSERT INTO rooms(id, membership) VALUES(?, '') ON CONFLICT(id) DO NOTHING",
		string(roomID)); err != nil {
		return fmt.Errorf("db: register room %s: %w", roomID, err)
	}
	return nil
}

// SetArchived keeps which of rooms the network archived. A room the cache does not
// hold yet is registered (registerRoom), as a message's is: a network may say a chat
// is archived before anything lists it (WhatsApp's first sync).
func (c *Cache) SetArchived(ctx context.Context, rooms map[domain.RoomID]bool) error {
	if len(rooms) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for id, archived := range rooms {
			query := `DELETE FROM room_archived WHERE room_id = ?`
			if archived {
				if err := registerRoom(ctx, tx, id); err != nil {
					return err
				}
				query = `INSERT INTO room_archived(room_id) VALUES(?) ON CONFLICT(room_id) DO NOTHING`
			}
			if _, err := tx.ExecContext(ctx, query, string(id)); err != nil {
				return fmt.Errorf("db: keep %s archived=%v: %w", id, archived, err)
			}
		}
		return nil
	})
}

// SetForums keeps which of rooms are made of topics, as a network's listing says.
func (c *Cache) SetForums(ctx context.Context, rooms map[domain.RoomID]bool) error {
	if len(rooms) == 0 {
		return nil
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for id, forum := range rooms {
			query := `DELETE FROM room_forums WHERE room_id = ?`
			if forum {
				if err := registerRoom(ctx, tx, id); err != nil {
					return err
				}
				query = `INSERT INTO room_forums(room_id) VALUES(?) ON CONFLICT(room_id) DO NOTHING`
			}
			if _, err := tx.ExecContext(ctx, query, string(id)); err != nil {
				return fmt.Errorf("db: keep %s forum=%v: %w", id, forum, err)
			}
		}
		return nil
	})
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Stored rooms.membership values; empty is a registered placeholder (registerRoom).
// Spelled out because domain.MembershipJoin is the empty string.
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

// SaveRooms reconciles owner's cached joined rooms with rooms in one transaction:
// upsert each, then delete the ones of owner's that are gone (cascading their data).
// Only owner's: the cache holds every network's rooms, and one network's list says
// nothing about another's. A plain replace would cascade away every room's history.
// An empty snapshot sweeps nothing. A room owner does not own is refused.
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
		return sweepOwnedRooms(ctx, tx, owner, rooms)
	})
}

// AddRooms upserts some of owner's joined rooms and sweeps nothing: a room a network
// learns of one at a time (a chat's first message), not from a full listing.
func (c *Cache) AddRooms(ctx context.Context, owner domain.RoomOwner, rooms []domain.Room) error {
	for i := range rooms {
		if !owner.Owns(rooms[i].ID) {
			return fmt.Errorf("db: %s is not among %q's rooms", rooms[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error { return upsertRooms(ctx, tx, rooms, membershipJoin) })
}

// JoinRooms makes owner's rooms joined — new, or placeholders a message registered —
// without touching what is known of one already joined (its name above all). It
// reports how many were not joined before.
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

// sweepOwnedRooms deletes owner's joined rooms the snapshot no longer carries. An
// owner is an ID prefix ("!" for Matrix, "whatsapp:<account>/" for an account), so
// the sweep matches on it and never reaches a room of another's.
func sweepOwnedRooms(ctx context.Context, tx *sql.Tx, owner domain.RoomOwner, rooms []domain.Room) error {
	if len(rooms) == 0 {
		return nil
	}
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	in, args := inIDs([]any{membershipJoin, len(owner), string(owner)}, ids)
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

// sweepRooms deletes the rooms of one membership the snapshot no longer carries.
func sweepRooms(ctx context.Context, tx *sql.Tx, rooms []domain.Room, membership string) error {
	if len(rooms) == 0 {
		return nil
	}
	ids := make([]domain.RoomID, len(rooms))
	for i := range rooms {
		ids[i] = rooms[i].ID
	}
	in, args := inIDs([]any{membership}, ids)
	// #nosec G202 -- inIDs emits only placeholders or a bound json_each.
	query := "DELETE FROM rooms WHERE membership = ? AND id NOT" + in
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("db: sweep %s rooms: %w", membership, err)
	}
	return nil
}

// Spaces returns the cached spaces with their child rooms, sorted for display.
func (c *Cache) Spaces(ctx context.Context) ([]domain.Space, error) {
	spaces, err := collect(ctx, c.db, "spaces", "SELECT id, name, bridge, keeper FROM spaces",
		func(rows *sql.Rows) (domain.Space, error) {
			var id, name, bridge, keeper string
			err := rows.Scan(&id, &name, &bridge, &keeper)
			return domain.Space{
				ID:     domain.SpaceID(id),
				Name:   name,
				Bridge: domain.Protocol(bridge),
				Keeper: keeper,
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

// SaveSpaces replaces owner's cached spaces and their child mappings in one
// transaction, leaving every other network's (and account's) spaces alone. A space
// owner does not own is refused.
func (c *Cache) SaveSpaces(ctx context.Context, owner domain.RoomOwner, spaces []domain.Space) error {
	for i := range spaces {
		if !owner.Owns(domain.RoomID(spaces[i].ID)) {
			return fmt.Errorf("db: space %s is not among %q's", spaces[i].ID, owner)
		}
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		// Children go with their space (ON DELETE CASCADE); fixed statements, the
		// owner bound as an ID prefix as SaveRooms binds it.
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
		for _, s := range spaces {
			if _, err := space.ExecContext(ctx, string(s.ID), s.Name, string(s.Bridge), s.Keeper); err != nil {
				return fmt.Errorf("db: insert space %s: %w", s.ID, err)
			}
			for pos, roomID := range s.Children {
				if _, err := child.ExecContext(ctx, string(s.ID), string(roomID), pos); err != nil {
					return fmt.Errorf("db: insert space child %s/%s: %w", s.ID, roomID, err)
				}
			}
		}
		return nil
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SpaceIDs is the set of cached space IDs (empty on a cold cache).
func (c *Cache) SpaceIDs(ctx context.Context) (map[domain.RoomID]bool, error) {
	return collectMap(ctx, c.db, "space ids", "SELECT id FROM spaces",
		func(rows *sql.Rows) (domain.RoomID, bool, error) {
			var id string
			err := rows.Scan(&id)
			return domain.RoomID(id), true, err
		})
}
