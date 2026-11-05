package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetPicture keeps a room's picture as its network gave it at: id its name for the
// picture, "" (or no data) for a room found to have none. A room merged into another is
// filed under that one; a room the cache does not hold is skipped, as a network tells of
// pictures of people no chat is with.
func (c *Cache) SetPicture(ctx context.Context, room domain.RoomID, id string, data []byte, at time.Time) error {
	if id == "" || len(data) == 0 {
		id, data = "", nil // a version always names bytes
	}
	return c.inTx(ctx, func(tx *sql.Tx) error {
		into, err := filedUnder(ctx, tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO room_pictures(room_id, picture_id, data, checked_ms)
			SELECT ?1, ?2, ?3, ?4 WHERE EXISTS (SELECT 1 FROM rooms WHERE id = ?1)
			ON CONFLICT(room_id) DO UPDATE SET
				picture_id = excluded.picture_id, data = excluded.data, checked_ms = excluded.checked_ms`,
			string(into), id, append([]byte{}, data...), at.UnixMilli()); err != nil {
			return fmt.Errorf("db: keep the picture of %s: %w", into, err)
		}
		return nil
	})
}

// PictureUnchanged marks a room's kept picture as still the network's at at.
func (c *Cache) PictureUnchanged(ctx context.Context, room domain.RoomID, at time.Time) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		into, err := filedUnder(ctx, tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE room_pictures SET checked_ms = ? WHERE room_id = ?`,
			at.UnixMilli(), string(into)); err != nil {
			return fmt.Errorf("db: mark the picture of %s checked: %w", into, err)
		}
		return nil
	})
}

// PictureChecks is what the cache knows of every room's picture it has asked about.
func (c *Cache) PictureChecks(ctx context.Context) (map[domain.RoomID]domain.PictureCheck, error) {
	return collectMap(ctx, c.db, "picture checks", `SELECT room_id, picture_id, checked_ms FROM room_pictures`,
		func(rows *sql.Rows) (domain.RoomID, domain.PictureCheck, error) {
			var (
				room, id string
				checked  int64
			)
			err := rows.Scan(&room, &id, &checked)
			return domain.RoomID(room), domain.PictureCheck{ID: id, Checked: time.UnixMilli(checked)}, err
		})
}

// Picture is a room's kept picture, the version domain.Room.Picture names; nil when the
// room has none.
func (c *Cache) Picture(ctx context.Context, room domain.RoomID) ([]byte, error) {
	var data []byte
	found, err := optional(c.db.QueryRowContext(ctx,
		`SELECT data FROM room_pictures WHERE room_id = ? AND picture_id <> ''`, string(room)).Scan(&data))
	if err != nil {
		return nil, fmt.Errorf("db: read the picture of %s: %w", room, err)
	}
	if !found {
		return nil, nil
	}
	return data, nil
}

// PersonPictures is the cache's people's pictures, kept as rooms' are, by the ID a
// person's messages name them by.
type PersonPictures struct{ c *Cache }

// PersonPictures is where people's pictures are kept.
func (c *Cache) PersonPictures() PersonPictures { return PersonPictures{c} }

// SetPicture keeps a person's picture as their network gave it at, as Cache.SetPicture
// keeps a room's; any person is kept, in a room the cache holds or not yet.
func (p PersonPictures) SetPicture(ctx context.Context, person, id string, data []byte, at time.Time) error {
	if id == "" || len(data) == 0 {
		id, data = "", nil // a version always names bytes
	}
	if _, err := p.c.db.ExecContext(ctx, `
		INSERT INTO person_pictures(person_id, picture_id, data, checked_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET
			picture_id = excluded.picture_id, data = excluded.data, checked_ms = excluded.checked_ms`,
		person, id, append([]byte{}, data...), at.UnixMilli()); err != nil {
		return fmt.Errorf("db: keep the picture of %s: %w", person, err)
	}
	return nil
}

// PictureUnchanged marks a person's kept picture as still the network's at at.
func (p PersonPictures) PictureUnchanged(ctx context.Context, person string, at time.Time) error {
	if _, err := p.c.db.ExecContext(ctx, `UPDATE person_pictures SET checked_ms = ? WHERE person_id = ?`,
		at.UnixMilli(), person); err != nil {
		return fmt.Errorf("db: mark the picture of %s checked: %w", person, err)
	}
	return nil
}

// PictureChecks is what the cache knows of every person's picture it has asked about.
func (p PersonPictures) PictureChecks(ctx context.Context) (map[string]domain.PictureCheck, error) {
	return collectMap(ctx, p.c.db, "people's picture checks", `SELECT person_id, picture_id, checked_ms FROM person_pictures`,
		func(rows *sql.Rows) (string, domain.PictureCheck, error) {
			var (
				person, id string
				checked    int64
			)
			err := rows.Scan(&person, &id, &checked)
			return person, domain.PictureCheck{ID: id, Checked: time.UnixMilli(checked)}, err
		})
}

// inRoom holds where person_pictures.person_id is someone in the room ?1: a member, or
// a sender of a message the room keeps.
const inRoom = `(person_id IN (SELECT user_id FROM room_members WHERE room_id = ?1)
	OR EXISTS (SELECT 1 FROM messages WHERE room_id = ?1 AND sender = person_id))`

// SenderPictures is the picture version of each person in room who has one, by their ID.
func (c *Cache) SenderPictures(ctx context.Context, room domain.RoomID) (map[string]string, error) {
	return collectMap(ctx, c.db, "the pictures of "+string(room)+"'s people",
		`SELECT person_id, picture_id FROM person_pictures WHERE picture_id <> '' AND `+inRoom, func(rows *sql.Rows) (string, string, error) {
			var person, id string
			err := rows.Scan(&person, &id)
			return person, id, err
		}, string(room))
}

// PersonPicture is the kept picture of a person in room, the version SenderPictures
// names; nil when they have none, or are no one there.
func (c *Cache) PersonPicture(ctx context.Context, room domain.RoomID, person string) ([]byte, error) {
	var data []byte
	found, err := optional(c.db.QueryRowContext(ctx,
		`SELECT data FROM person_pictures WHERE person_id = ?2 AND picture_id <> '' AND `+inRoom,
		string(room), person).Scan(&data))
	if err != nil {
		return nil, fmt.Errorf("db: read the picture of %s: %w", person, err)
	}
	if !found {
		return nil, nil
	}
	return data, nil
}
