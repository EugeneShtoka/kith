package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
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

// peopleInRoom is which of people are in room: a member, or a sender of a message it keeps.
func (c *Cache) peopleInRoom(ctx context.Context, room domain.RoomID, people []string) ([]string, error) {
	var in []string
	for _, p := range people {
		var there bool
		if err := c.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM room_members WHERE room_id = ?1 AND user_id = ?2)
			OR EXISTS (SELECT 1 FROM messages WHERE room_id = ?1 AND sender = ?2)`, string(room), p).Scan(&there); err != nil {
			return nil, fmt.Errorf("db: is %s in %s: %w", p, room, err)
		}
		if there {
			in = append(in, p)
		}
	}
	return in, nil
}

// versions is the kept picture version of each of people who has one.
func (p PersonPictures) versions(ctx context.Context, people []string) (map[string]string, error) {
	out := map[string]string{}
	for _, person := range people {
		var id string
		found, err := optional(p.c.db.QueryRowContext(ctx,
			`SELECT picture_id FROM person_pictures WHERE person_id = ? AND picture_id <> ''`, person).Scan(&id))
		if err != nil {
			return nil, fmt.Errorf("db: read the picture version of %s: %w", person, err)
		}
		if found {
			out[person] = id
		}
	}
	return out, nil
}

// picture is a person's kept picture wherever they are; nil when they have none. It is
// handed on only for someone in a room the caller may show.
func (p PersonPictures) picture(ctx context.Context, person string) ([]byte, error) {
	var data []byte
	found, err := optional(p.c.db.QueryRowContext(ctx,
		`SELECT data FROM person_pictures WHERE person_id = ? AND picture_id <> ''`, person).Scan(&data))
	if err != nil {
		return nil, fmt.Errorf("db: read the picture of %s: %w", person, err)
	}
	if !found {
		return nil, nil
	}
	return data, nil
}

// SenderPicturesAs is SenderPictures as faces have it: each person faces names who is in
// room shows the first picture of their chain (setup.FacesOf), under a version of its own
// when it is another's.
func (c *Cache) SenderPicturesAs(ctx context.Context, room domain.RoomID, faces map[string][]string) (map[string]string, error) {
	out, err := c.SenderPictures(ctx, room)
	if err != nil || len(faces) == 0 {
		return out, err
	}
	in, err := c.peopleInRoom(ctx, room, slices.Sorted(maps.Keys(faces)))
	if err != nil {
		return nil, err
	}
	for _, person := range in {
		source, version, err := c.standIn(ctx, faces[person])
		if err != nil {
			return nil, err
		}
		if source != "" && source != person { // none (their own is in the chain), or their own, as kept
			out[person] = "via " + source + " " + version
		}
	}
	return out, nil
}

// PersonPictureAs is PersonPicture as chain has it (setup.FacesOf; nil for someone no
// identity names): the first picture of it, for someone in room alone.
func (c *Cache) PersonPictureAs(ctx context.Context, room domain.RoomID, person string, chain []string) ([]byte, error) {
	if chain == nil {
		return c.PersonPicture(ctx, room, person)
	}
	if in, err := c.peopleInRoom(ctx, room, []string{person}); err != nil || len(in) == 0 {
		return nil, err // no one there is shown
	}
	source, _, err := c.standIn(ctx, chain)
	if err != nil || source == "" {
		return nil, err
	}
	return c.PersonPictures().picture(ctx, source)
}

// standIn is the first of chain that has a picture, and its version; "" for none.
func (c *Cache) standIn(ctx context.Context, chain []string) (source, version string, err error) {
	versions, err := c.PersonPictures().versions(ctx, chain)
	if err != nil {
		return "", "", err
	}
	for _, id := range chain {
		if v, ok := versions[id]; ok {
			return id, v, nil
		}
	}
	return "", "", nil
}
