package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Read positions cross bridges (planning/design-mesh.md, Read positions): a read made
// here is written to the mesh from what the cache holds once the network's adapter
// kept it (HeldReads), and one made on another bridge is kept here as the mesh holds it
// (KeepReads), with no receipt: the bridge where it was made sends that.

// HeldReads is how far each room or thread of of (Room and Root; Event and At are
// not read) is read here, a room merged into another as the room it joined; one
// whose position is not known is left out.
func (c *Cache) HeldReads(ctx context.Context, of []domain.ReadPosition) ([]domain.ReadPosition, error) {
	var out []domain.ReadPosition
	for _, q := range of {
		var room, event string
		var at int64
		var query string
		args := []any{string(q.Room), string(q.Room)}
		if q.Root == "" {
			query = `SELECT u.room_id, u.read_event, ` + readPosition + ` FROM room_unread u
			  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event
			 WHERE u.room_id = coalesce((SELECT into_id FROM room_aliases WHERE room_id = ?), ?) AND` + readKnown
		} else {
			query = `SELECT room_id, read_event, read_ts_ms FROM thread_read
			 WHERE room_id = coalesce((SELECT into_id FROM room_aliases WHERE room_id = ?), ?) AND root_event = ?`
			args = append(args, string(q.Root))
		}
		found, err := optional(c.db.QueryRowContext(ctx, query, args...).Scan(&room, &event, &at))
		if err != nil {
			return nil, fmt.Errorf("db: the read position of %s: %w", q.Room, err)
		}
		if found {
			out = append(out, domain.ReadPosition{Room: domain.RoomID(room), Root: q.Root, Event: domain.EventID(event), At: at})
		}
	}
	return out, nil
}

// heldReads is what KeepReads compares against: the rooms the cache holds, each
// room merged into another and the room it joined, and each room's read position
// (its time) and mark, and each thread's.
type heldReads struct {
	rooms   map[domain.RoomID]bool
	aliases map[domain.RoomID]domain.RoomID
	at      map[domain.RoomID]int64
	marked  map[domain.RoomID]bool
	threads map[[2]string]domain.ReadPosition
}

// readHeld reads what KeepReads compares against.
func (c *Cache) readHeld(ctx context.Context) (heldReads, error) {
	h := heldReads{rooms: map[domain.RoomID]bool{}, at: map[domain.RoomID]int64{}, marked: map[domain.RoomID]bool{}, threads: map[[2]string]domain.ReadPosition{}}
	rooms, err := collect(ctx, c.db, "rooms", `SELECT id FROM rooms`, func(rows *sql.Rows) (string, error) {
		var id string
		err := rows.Scan(&id)
		return id, err
	})
	if err != nil {
		return heldReads{}, err
	}
	for _, id := range rooms {
		h.rooms[domain.RoomID(id)] = true
	}
	if h.aliases, err = c.RoomAliases(ctx); err != nil {
		return heldReads{}, err
	}
	type roomRow struct {
		room   string
		at     int64
		marked bool
	}
	rows, err := collect(ctx, c.db, "read positions", `SELECT u.room_id, `+readPosition+`, u.marked FROM room_unread u
		  LEFT JOIN messages r ON r.room_id = u.room_id AND r.event_id = u.read_event`,
		func(rows *sql.Rows) (roomRow, error) {
			var r roomRow
			return r, rows.Scan(&r.room, &r.at, &r.marked)
		})
	if err != nil {
		return heldReads{}, err
	}
	for _, r := range rows {
		h.at[domain.RoomID(r.room)], h.marked[domain.RoomID(r.room)] = r.at, r.marked
	}
	threads, err := collect(ctx, c.db, "thread read positions", `SELECT room_id, root_event, read_event, read_ts_ms FROM thread_read`,
		func(rows *sql.Rows) (domain.ReadPosition, error) {
			var p domain.ReadPosition
			return p, rows.Scan(&p.Room, &p.Root, &p.Event, &p.At)
		})
	if err != nil {
		return heldReads{}, err
	}
	for _, p := range threads {
		h.threads[[2]string{string(p.Room), string(p.Root)}] = p
	}
	return h, nil
}

// KeepReads moves read positions forward to reads, and sets unread marks to marks, as
// made on other bridges (the mesh's). A room the cache does not hold is left alone (one
// of an account this bridge does not run), and one merged into another is filed under
// it. A position at or before the one held changes nothing; one further starts the
// countdown of the disappearing messages it reads through, as a read here does. moved
// are the rooms whose position or mark changed.
func (c *Cache) KeepReads(ctx context.Context, reads []domain.ReadPosition, marks map[domain.RoomID]bool) ([]domain.RoomID, error) {
	h, err := c.readHeld(ctx)
	if err != nil {
		return nil, err
	}
	k := h.toKeep(reads, marks)
	if len(k.rooms)+len(k.threads)+len(k.marked) == 0 {
		return nil, nil
	}
	moved := map[domain.RoomID]bool{}
	if err := c.inTx(ctx, func(tx *sql.Tx) error { return k.write(ctx, tx, moved) }); err != nil {
		return nil, err
	}
	out := make([]domain.RoomID, 0, len(moved))
	for room := range moved {
		out = append(out, room)
	}
	return out, nil
}

// readsToKeep are the room and thread positions, and the marks, that move the cache.
type readsToKeep struct {
	rooms, threads []domain.ReadPosition
	marked         map[domain.RoomID]bool
}

// filed is the room a write naming room goes to here, and whether the cache holds it.
func (h heldReads) filed(room domain.RoomID) (domain.RoomID, bool) {
	if into, ok := h.aliases[room]; ok {
		room = into
	}
	return room, h.rooms[room]
}

// toKeep is what of reads and marks moves the cache past what h holds.
func (h heldReads) toKeep(reads []domain.ReadPosition, marks map[domain.RoomID]bool) readsToKeep {
	k := readsToKeep{marked: map[domain.RoomID]bool{}}
	for _, r := range reads {
		var held bool
		if r.Room, held = h.filed(r.Room); !held {
			continue
		}
		if r.Root == "" {
			if r.At > h.at[r.Room] {
				k.rooms, h.at[r.Room] = append(k.rooms, r), r.At
			}
			continue
		}
		key := [2]string{string(r.Room), string(r.Root)}
		if was, ok := h.threads[key]; !ok || r.At > was.At || (r.At == was.At && r.Event > was.Event) {
			k.threads, h.threads[key] = append(k.threads, r), r
		}
	}
	for room, m := range marks {
		if into, held := h.filed(room); held && h.marked[into] != m {
			k.marked[into] = m
		}
	}
	return k
}

// write keeps k inside tx, adding the rooms it moves to moved.
func (k readsToKeep) write(ctx context.Context, tx *sql.Tx, moved map[domain.RoomID]bool) error {
	for _, r := range k.rooms {
		if err := keepRead(ctx, tx, r); err != nil {
			return err
		}
		moved[r.Room] = true
	}
	for _, r := range k.threads {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO thread_read(room_id, root_event, read_event, read_ts_ms) VALUES(?, ?, ?, ?)
			ON CONFLICT(room_id, root_event) DO UPDATE SET read_event = excluded.read_event, read_ts_ms = excluded.read_ts_ms
			WHERE (excluded.read_ts_ms, excluded.read_event) > (thread_read.read_ts_ms, thread_read.read_event)`,
			string(r.Room), string(r.Root), string(r.Event), r.At); err != nil {
			return fmt.Errorf("db: keep the read position of a thread of %s: %w", r.Room, err)
		}
		moved[r.Room] = true
	}
	for room, m := range k.marked {
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_unread(room_id, marked) VALUES(?, ?)
			ON CONFLICT(room_id) DO UPDATE SET marked = excluded.marked`, string(room), m); err != nil {
			return fmt.Errorf("db: keep the unread mark of %s: %w", room, err)
		}
		moved[room] = true
	}
	return nil
}

// keepRead moves r's room's position forward to r, inside tx, unless a network moved
// it as far meanwhile, and starts the countdowns it reads through.
func keepRead(ctx context.Context, tx *sql.Tx, r domain.ReadPosition) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO room_unread(room_id, read_event, read_ts_ms) VALUES(?, ?, ?)
		ON CONFLICT(room_id) DO UPDATE SET read_event = excluded.read_event, read_ts_ms = excluded.read_ts_ms
		WHERE excluded.read_ts_ms > max(room_unread.read_ts_ms, coalesce((SELECT m.ts_ms FROM messages m
		      WHERE m.room_id = room_unread.room_id AND m.event_id = room_unread.read_event), 0))`,
		string(r.Room), string(r.Event), r.At); err != nil {
		return fmt.Errorf("db: keep the read position of %s: %w", r.Room, err)
	}
	return startCountdowns(ctx, tx, r.Room, time.Now())
}
