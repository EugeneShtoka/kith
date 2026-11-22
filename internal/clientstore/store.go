// Package clientstore is kith-proxy's store of one bridge's answers on a machine that
// reads that bridge from elsewhere: a cache, every row a copy of something the bridge
// answered, so the clients here can read it while the bridge is out of reach. It is
// sealed as kithd's cache is (internal/sealeddb), holds no network's session, and is
// rebuilt rather than migrated: a file of another layout, or one its key does not
// open, is deleted and filled again.
//
// What it keeps is opaque to it: messages and answers are the bytes the proxy gives
// it, keyed by room, method, profile and request. Rooms are kept by integer key, each
// network ID once.
package clientstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// layout is the schema's version (PRAGMA user_version); a file of another is rebuilt.
const layout = 1

const schema = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE rooms (
	id     INTEGER PRIMARY KEY,
	room   TEXT NOT NULL UNIQUE,
	filled INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
	room    INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event   TEXT NOT NULL,
	at      INTEGER NOT NULL,
	seq     INTEGER NOT NULL,
	message BLOB NOT NULL,
	PRIMARY KEY (room, event)
) WITHOUT ROWID;
CREATE INDEX messages_at ON messages(room, at);
CREATE INDEX messages_seq ON messages(seq);
CREATE TABLE answers (
	method   TEXT NOT NULL,
	profile  TEXT NOT NULL,
	request  TEXT NOT NULL,
	response BLOB NOT NULL,
	PRIMARY KEY (method, profile, request)
) WITHOUT ROWID;
CREATE TABLE room_answers (
	method   TEXT NOT NULL,
	room     INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	response BLOB NOT NULL,
	PRIMARY KEY (method, room)
) WITHOUT ROWID;
CREATE TABLE sets (
	profile TEXT NOT NULL,
	room    INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	PRIMARY KEY (profile, room)
) WITHOUT ROWID;
CREATE TABLE positions (
	stream TEXT PRIMARY KEY,
	epoch  TEXT NOT NULL,
	change INTEGER NOT NULL
) WITHOUT ROWID;
`

// setup is the store's connections: the cache's own (internal/db), without its
// full-text extension.
var setup = sealeddb.Setup{Pragmas: []string{"synchronous(NORMAL)"}, TxLock: "immediate"}

// Store is one bridge's answers on this machine. Its methods are safe for concurrent
// use; one process writes it (kith-proxy holds its lock).
type Store struct {
	db    *sql.DB
	epoch string
}

// Open opens the store at path with key, making it when there is none, and rebuilding
// it empty when its layout is another or key does not open it.
func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	s, err := open(ctx, path, key)
	if errors.Is(err, errRebuild) || errors.Is(err, sealeddb.ErrWrongKey) {
		if rerr := Remove(path); rerr != nil {
			return nil, rerr
		}
		s, err = open(ctx, path, key)
	}
	return s, err
}

// errRebuild is a file of another layout.
var errRebuild = errors.New("clientstore: another layout")

func open(ctx context.Context, path string, key []byte) (*Store, error) {
	db, err := sealeddb.OpenWith(ctx, path, key, setup)
	if err != nil {
		return nil, fmt.Errorf("clientstore: %w", err)
	}
	s := &Store{db: db}
	if err := s.init(ctx); err != nil {
		_ = db.Close() // the init failure is the report
		return nil, err
	}
	return s, nil
}

// init makes a new file's schema and epoch, or checks an existing one's layout.
func (s *Store) init(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("clientstore: read the layout: %w", err)
	}
	switch version {
	case layout:
		if err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'epoch'").Scan(&s.epoch); err != nil {
			return fmt.Errorf("%w: no epoch: %w", errRebuild, err)
		}
		return nil
	case 0:
	default:
		return fmt.Errorf("%w: %d", errRebuild, version)
	}
	epoch := make([]byte, 8)
	if _, err := rand.Read(epoch); err != nil {
		return fmt.Errorf("clientstore: an epoch: %w", err)
	}
	s.epoch = "proxy-" + hex.EncodeToString(epoch)
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("clientstore: make the schema: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('epoch', ?)", s.epoch); err != nil {
			return fmt.Errorf("clientstore: keep the epoch: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", layout)); err != nil {
			return fmt.Errorf("clientstore: mark the layout: %w", err)
		}
		return nil
	})
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("clientstore: close: %w", err)
	}
	return nil
}

// Remove deletes the store at path, its log with it; none there is no error.
func Remove(path string) error {
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clientstore: delete %s: %w", f, err)
		}
	}
	return nil
}

// Epoch names this store's change numbering; a new file numbers anew.
func (s *Store) Epoch() string { return s.epoch }

func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clientstore: begin: %w", err)
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback() // f's error is the report
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clientstore: commit: %w", err)
	}
	return nil
}

// roomID is room's integer key, made when it has none.
func roomID(ctx context.Context, tx *sql.Tx, room string) (int64, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO rooms (room) VALUES (?) ON CONFLICT (room) DO NOTHING", room); err != nil {
		return 0, fmt.Errorf("clientstore: keep room %s: %w", room, err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM rooms WHERE room = ?", room).Scan(&id); err != nil {
		return 0, fmt.Errorf("clientstore: room %s: %w", room, err)
	}
	return id, nil
}

// Message is one message as the bridge last sent it: its event ID, its time, and the
// bytes the proxy keeps of it.
type Message struct {
	Event   string
	At      time.Time
	Message []byte
}

// Seq is the last number this store gave a change.
func (s *Store) Seq(ctx context.Context) (int64, error) { return lastSeq(ctx, s.db) }

// PutStream writes msgs of room, each as the newest word on it, numbering each a
// change, with the stream's position in the same transaction: a store stopped
// mid-write resumes where it stood.
func (s *Store) PutStream(ctx context.Context, room string, msgs []Message, stream, epoch string, change int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := put(ctx, tx, room, msgs, math.MaxInt64); err != nil {
			return err
		}
		return putPosition(ctx, tx, stream, epoch, change)
	})
}

// Asked is where the store stands as a page of history is asked for: its last change,
// and how many times its rooms were marked to fill again (Unfill).
type Asked struct{ seq, unfills int64 }

// Ask is where the store stands now, for a page about to be asked for.
func (s *Store) Ask(ctx context.Context) (Asked, error) {
	seq, err := lastSeq(ctx, s.db)
	if err != nil {
		return Asked{}, err
	}
	unfills, err := counter(ctx, s.db, unfillsKey)
	return Asked{seq: seq, unfills: unfills}, err
}

// PutPage writes a page of room's history asked for at asked: a message a change
// numbered after that keeps its row, being newer word than the page's. The room is
// marked filled, unless its rooms were marked to fill again since the page was asked:
// the page is older than what made them so.
func (s *Store) PutPage(ctx context.Context, room string, msgs []Message, asked Asked) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := put(ctx, tx, room, msgs, asked.seq); err != nil {
			return err
		}
		unfills, err := counter(ctx, tx, unfillsKey)
		if err != nil || unfills != asked.unfills {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE rooms SET filled = 1 WHERE room = ?", room); err != nil {
			return fmt.Errorf("clientstore: mark room %s filled: %w", room, err)
		}
		return nil
	})
}

// put writes msgs of room, each numbered the next change, over a row numbered at or
// before keep.
func put(ctx context.Context, tx *sql.Tx, room string, msgs []Message, keep int64) error {
	if len(msgs) == 0 {
		return nil
	}
	id, err := roomID(ctx, tx, room)
	if err != nil {
		return err
	}
	next, err := lastSeq(ctx, tx)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		next++
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages (room, event, at, seq, message) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (room, event) DO UPDATE SET at = excluded.at, seq = excluded.seq, message = excluded.message
			WHERE messages.seq <= ?`, id, m.Event, m.At.UnixMilli(), next, m.Message, keep); err != nil {
			return fmt.Errorf("clientstore: keep message %s: %w", m.Event, err)
		}
	}
	return putCounter(ctx, tx, seqKey, next)
}

const (
	// seqKey keeps the last number a change was given, apart from the rows, so a room
	// deleted never gives its numbers again.
	seqKey = "seq"
	// unfillsKey counts the times every room was marked to fill again.
	unfillsKey = "unfills"
)

// querier is a database or a transaction, to read from.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// lastSeq is the last number a change was given.
func lastSeq(ctx context.Context, q querier) (int64, error) { return counter(ctx, q, seqKey) }

// counter is the number kept under key, 0 when none.
func counter(ctx context.Context, q querier, key string) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("clientstore: %s: %w", key, err)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("clientstore: %s %q: %w", key, v, err)
	}
	return n, nil
}

func putCounter(ctx context.Context, tx *sql.Tx, key string, n int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, strconv.FormatInt(n, 10)); err != nil {
		return fmt.Errorf("clientstore: keep %s: %w", key, err)
	}
	return nil
}

// Timeline is room's newest limit messages, oldest first.
func (s *Store) Timeline(ctx context.Context, room string, limit int) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT message FROM (
		SELECT m.message, m.at, m.seq FROM messages m JOIN rooms r ON r.id = m.room
		WHERE r.room = ? ORDER BY m.at DESC, m.seq DESC LIMIT ?) ORDER BY at, seq`, room, limit)
	if err != nil {
		return nil, fmt.Errorf("clientstore: room %s: %w", room, err)
	}
	return blobs(rows)
}

// Change is one message as a change numbered it: the number, the room, the bytes.
type Change struct {
	Seq     int64
	Room    string
	Message []byte
}

// ChangesAfter is the messages numbered after seq, in order, at most limit.
func (s *Store) ChangesAfter(ctx context.Context, seq int64, limit int) ([]Change, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.seq, r.room, m.message FROM messages m JOIN rooms r ON r.id = m.room
		WHERE m.seq > ? ORDER BY m.seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, fmt.Errorf("clientstore: changes after %d: %w", seq, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Seq, &c.Room, &c.Message); err != nil {
			return nil, fmt.Errorf("clientstore: a change: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clientstore: changes after %d: %w", seq, err)
	}
	return out, nil
}

// Unfilled is the rooms some profile holds whose history no page has filled yet.
func (s *Store) Unfilled(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT room FROM rooms WHERE filled = 0
		AND id IN (SELECT room FROM sets) ORDER BY room`)
	if err != nil {
		return nil, fmt.Errorf("clientstore: rooms to fill: %w", err)
	}
	return texts(rows)
}

// Unfill marks every room's history to be filled again: what the store holds of it
// may be behind the bridge's (its stream could not resume, or names changed).
func (s *Store) Unfill(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE rooms SET filled = 0"); err != nil {
			return fmt.Errorf("clientstore: mark rooms to fill: %w", err)
		}
		n, err := counter(ctx, tx, unfillsKey)
		if err != nil {
			return err
		}
		return putCounter(ctx, tx, unfillsKey, n+1)
	})
}

// Answer is the response kept for method asked in profile with request, if any.
func (s *Store) Answer(ctx context.Context, method, profile, request string) ([]byte, bool, error) {
	var response []byte
	err := s.db.QueryRowContext(ctx, "SELECT response FROM answers WHERE method = ? AND profile = ? AND request = ?",
		method, profile, request).Scan(&response)
	return found(response, err, method)
}

// PutAnswer keeps response as method's answer in profile to request.
func (s *Store) PutAnswer(ctx context.Context, method, profile, request string, response []byte) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO answers (method, profile, request, response) VALUES (?, ?, ?, ?)
		ON CONFLICT (method, profile, request) DO UPDATE SET response = excluded.response`,
		method, profile, request, response); err != nil {
		return fmt.Errorf("clientstore: keep %s: %w", method, err)
	}
	return nil
}

// RoomAnswer is the response kept for method about room, if any.
func (s *Store) RoomAnswer(ctx context.Context, method, room string) ([]byte, bool, error) {
	var response []byte
	err := s.db.QueryRowContext(ctx, `SELECT a.response FROM room_answers a JOIN rooms r ON r.id = a.room
		WHERE a.method = ? AND r.room = ?`, method, room).Scan(&response)
	return found(response, err, method)
}

// PutRoomAnswer keeps response as method's answer about room, whichever profile asked.
func (s *Store) PutRoomAnswer(ctx context.Context, method, room string, response []byte) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		id, err := roomID(ctx, tx, room)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_answers (method, room, response) VALUES (?, ?, ?)
			ON CONFLICT (method, room) DO UPDATE SET response = excluded.response`, method, id, response); err != nil {
			return fmt.Errorf("clientstore: keep %s of %s: %w", method, room, err)
		}
		return nil
	})
}

// SetRooms makes rooms profile's set, and deletes every room no profile's set holds
// any more, with all it kept of it.
func (s *Store) SetRooms(ctx context.Context, profile string, rooms []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sets WHERE profile = ?", profile); err != nil {
			return fmt.Errorf("clientstore: clear %s's rooms: %w", profile, err)
		}
		for _, room := range rooms {
			id, err := roomID(ctx, tx, room)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sets (profile, room) VALUES (?, ?)", profile, id); err != nil {
				return fmt.Errorf("clientstore: keep %s's room %s: %w", profile, room, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM rooms WHERE id NOT IN (SELECT room FROM sets)"); err != nil {
			return fmt.Errorf("clientstore: delete rooms no profile holds: %w", err)
		}
		return nil
	})
}

// Holds reports whether profile's set holds room.
func (s *Store) Holds(ctx context.Context, profile, room string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sets s JOIN rooms r ON r.id = s.room
		WHERE s.profile = ? AND r.room = ?`, profile, room).Scan(&n); err != nil {
		return false, fmt.Errorf("clientstore: does %s hold %s: %w", profile, room, err)
	}
	return n > 0, nil
}

// Position is where the stream named stream stood with the bridge; "" when nowhere.
func (s *Store) Position(ctx context.Context, stream string) (epoch string, change int64, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT epoch, change FROM positions WHERE stream = ?", stream).Scan(&epoch, &change)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("clientstore: where %s stood: %w", stream, err)
	}
	return epoch, change, nil
}

func putPosition(ctx context.Context, tx *sql.Tx, stream, epoch string, change int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO positions (stream, epoch, change) VALUES (?, ?, ?)
		ON CONFLICT (stream) DO UPDATE SET epoch = excluded.epoch, change = excluded.change`, stream, epoch, change); err != nil {
		return fmt.Errorf("clientstore: keep where %s stands: %w", stream, err)
	}
	return nil
}

// Meta is the value kept under key, "" when none.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("clientstore: %s: %w", key, err)
	}
	return v, nil
}

// PutMeta keeps value under key.
func (s *Store) PutMeta(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("clientstore: keep %s: %w", key, err)
	}
	return nil
}

func found(response []byte, err error, method string) ([]byte, bool, error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("clientstore: %s: %w", method, err)
	}
	return response, true, nil
}

func blobs(rows *sql.Rows) ([][]byte, error) {
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("clientstore: a row: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clientstore: rows: %w", err)
	}
	return out, nil
}

func texts(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("clientstore: a row: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clientstore: rows: %w", err)
	}
	return out, nil
}
