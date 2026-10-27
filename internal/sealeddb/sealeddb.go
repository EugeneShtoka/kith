// Package sealeddb opens SQLite databases encrypted at rest: the network stores, whose
// contents act as the account (device identity keys, prekeys, sessions and sender
// keys), and the message cache. Every page is encrypted, the write-ahead log too, with
// a 32-byte key kept only in the OS keyring (session.LoadOrCreateStoreKey), so
// whatever a library writes into its store is covered without the library knowing.
//
// It is SQLite translated to Go from WebAssembly (ncruces/go-sqlite3, under wasm2go)
// with the Adiantum VFS: pure Go, so every binary still builds without cgo, and no
// runtime maps memory writable and executable — kithd's systemd unit forbids that
// (MemoryDenyWriteExecute).
package sealeddb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

// Databases are opened with driver.Open, never by a registered driver name: with cgo
// on (the race detector), mautrix's cryptohelper links mattn/go-sqlite3, which also
// registers "sqlite3", and the second registration panics. So the test targets clear
// ncruces' name (-X github.com/ncruces/go-sqlite3/driver.driverName=, in the
// Makefile); release builds, without cgo, link no mattn and need nothing.

// KeyLen is a store key's length in bytes.
const KeyLen = 32

var (
	// ErrWrongKey is a store the key does not open: another key's, or not a database.
	// It is never answered by making a new store: the old one holds the account.
	ErrWrongKey = errors.New("sealeddb: the key does not open this store")
	errKeyLen   = fmt.Errorf("sealeddb: a store key is %d bytes", KeyLen)
)

// Dialect is what whatsmeow and its dbutil call these databases.
const Dialect = "sqlite3"

// Setup is what a database's connections need beyond the defaults (dsn): more pragmas,
// a transaction mode ("immediate"), Init, run on each new connection, to register an
// extension its schema uses, and ReadOnly, to only read a database another process
// may be writing. The zero Setup is the network stores'.
type Setup struct {
	Pragmas  []string
	TxLock   string
	Init     func(*sqlite3.Conn) error
	ReadOnly bool
}

// open opens name with s's Init on each connection.
func (s Setup) open(name string) (*sql.DB, error) {
	var init []func(*sqlite3.Conn) error
	if s.Init != nil {
		init = append(init, s.Init)
	}
	db, err := driver.Open(name, init...)
	if err != nil {
		return nil, fmt.Errorf("sealeddb: %w", err)
	}
	return db, nil
}

// Open opens (creating) the encrypted database at path with key, and checks the key
// opens it: a wrong key is ErrWrongKey, at once rather than at the first query.
func Open(ctx context.Context, path string, key []byte) (*sql.DB, error) {
	return OpenWith(ctx, path, key, Setup{})
}

// OpenWith is Open with s's connection setup.
func OpenWith(ctx context.Context, path string, key []byte, s Setup) (*sql.DB, error) {
	if len(key) != KeyLen {
		return nil, errKeyLen
	}
	if !s.ReadOnly {
		if err := Private(path); err != nil {
			return nil, err
		}
	}
	db, err := s.open(s.dsn(path, key))
	if err != nil {
		return nil, fmt.Errorf("sealeddb: open %s: %w", path, err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&tables); err != nil {
		_ = db.Close()
		if errors.Is(err, sqlite3.NOTADB) {
			return nil, fmt.Errorf("%w: %s", ErrWrongKey, path)
		}
		return nil, fmt.Errorf("sealeddb: read %s: %w", path, err)
	}
	if !s.ReadOnly {
		// The read made the log and shared memory, with the umask's permissions (the VFS
		// does not take the database's); what they hold is encrypted, but keep them the
		// owner's like the database.
		for _, f := range []string{path + "-wal", path + "-shm"} {
			if err := os.Chmod(f, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = db.Close()
				return nil, fmt.Errorf("sealeddb: keep %s private: %w", f, err)
			}
		}
	}
	return db, nil
}

// dsn is the data source name of the encrypted database at path with key: the
// Adiantum VFS, temporary files kept in memory (so they are never written in the
// clear or under a throwaway key), foreign keys on, the write-ahead log, and a busy
// timeout, as the plain stores had; then s's pragmas and transaction mode. The timeout
// comes first, so a second opener switching a new store to the log waits rather than
// failing. Read-only, it keeps the timeout and the temporary files in memory, and
// changes nothing else.
func (s Setup) dsn(path string, key []byte) string {
	q := url.Values{
		"vfs":     {"adiantum"},
		"hexkey":  {hex.EncodeToString(key)},
		"_pragma": append([]string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "temp_store(memory)"}, s.Pragmas...),
	}
	if s.ReadOnly {
		q["_pragma"] = []string{"busy_timeout(5000)", "temp_store(memory)"}
		q.Set("mode", "ro")
	}
	if s.TxLock != "" {
		q.Set("_txlock", s.TxLock)
	}
	return (&url.URL{Scheme: "file", Opaque: escaped(path), RawQuery: q.Encode()}).String()
}

// escaped is path for a URI, so a space or "?" in it is not read as the query.
func escaped(path string) string { return (&url.URL{Path: path}).EscapedPath() }

// Target is what `VACUUM INTO ?` takes to write an encrypted copy of a database to
// path under key; a bare path would write the copy in the clear. Make path Private
// first.
func Target(path string, key []byte) string { return Setup{}.dsn(path, key) }

// Private creates path empty and the owner's alone, unless something is there: SQLite
// would make it, and its log and shared memory after it, with the umask's permissions,
// readable by everyone under the usual 022. An existing file is left as it is.
func Private(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the store's own path
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sealeddb: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sealeddb: create %s: %w", path, err)
	}
	return nil
}

// plainHeader starts every plain SQLite database; an encrypted one starts with noise.
const plainHeader = "SQLite format 3\x00"

// Suffixes of the files an interrupted migration can leave beside path.
const (
	sealingSuffix = ".sealing" // the encrypted copy being written
	plainSuffix   = ".plain"   // the plain store, set aside while the copy takes its name
)

// OpenMigrating is Open, first turning a plain store left at path (one written before
// stores were encrypted) into an encrypted one, one way: the plain store is copied,
// the copy checked against it table by table, then put in its place, and only then is
// the plain store deleted. An interrupted migration is taken up again at the next
// open; the plain store is never lost, and a copy not yet checked is never taken.
func OpenMigrating(ctx context.Context, path string, key []byte) (*sql.DB, error) {
	if err := Seal(ctx, path, key, Setup{}); err != nil {
		return nil, err
	}
	return Open(ctx, path, key)
}

// Seal is OpenMigrating without the open, with s's connection setup: a plain database
// at path is encrypted in
// its place, an interrupted migration taken up, and anything else left as it is.
// Openers racing on one plain database take turns (migrating), so none drops the copy
// another is writing or renames over another.
func Seal(ctx context.Context, path string, key []byte, s Setup) error {
	if len(key) != KeyLen {
		return errKeyLen
	}
	if done, err := migrated(path); err != nil || done {
		return err
	}
	unlock, lockErr := migrating(ctx, path)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if err := resume(path); err != nil {
		return err
	}
	plain, err := IsPlain(path)
	if err != nil || !plain {
		return err
	}
	return s.migrate(ctx, path, key)
}

// migrated reports whether path needs no migration: not plain, and nothing an
// interrupted one left beside it.
func migrated(path string) (bool, error) {
	for _, left := range []string{path + plainSuffix, path + sealingSuffix} {
		if _, err := os.Lstat(left); err == nil {
			return false, nil
		}
	}
	plain, err := IsPlain(path)
	return !plain, err
}

// lockSuffix names the lock a migration of path holds, beside it.
const lockSuffix = ".sealing-lock"

// migrating waits for and takes the lock on migrating path, returning what gives it up
// and deletes it. The lock is deleted only once the migration is done, so an opener
// still waiting on the deleted one finds path encrypted when it gets it, and a later
// one never needs a lock.
func migrating(ctx context.Context, path string) (unlock func(), err error) {
	lock := flock.New(path + lockSuffix)
	if _, err := lock.TryLockContext(ctx, lockRetry); err != nil {
		return nil, fmt.Errorf("sealeddb: wait for another migration of %s: %w", path, err)
	}
	return func() {
		_ = os.Remove(path + lockSuffix)
		_ = lock.Unlock()
	}, nil
}

// lockRetry is how often a migration waiting on another tries the lock.
const lockRetry = 50 * time.Millisecond

// resume settles what an interrupted migration left: a plain store set aside while
// its copy took its name is put back when the copy never did, and deleted when it did;
// a copy being written is dropped (it is written again from the plain store).
func resume(path string) error {
	setAside := path + plainSuffix
	if _, err := os.Stat(setAside); err == nil {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(setAside, path); err != nil {
				return fmt.Errorf("sealeddb: put the plain store back: %w", err)
			}
		} else if err := removeAll(setAside); err != nil {
			return err
		}
	}
	return removeAll(path + sealingSuffix)
}

// IsPlain reports whether path holds a plain SQLite database: one Seal would encrypt.
func IsPlain(path string) (bool, error) {
	f, err := os.Open(path) //nolint:gosec // the store's own path, from the daemon's storage
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sealeddb: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(plainHeader))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("sealeddb: read %s: %w", path, err)
	}
	return string(head[:n]) == plainHeader, nil
}

// migrate copies the plain store at path into an encrypted one, checks the copy, and
// puts it in the plain store's place.
func (s Setup) migrate(ctx context.Context, path string, key []byte) error {
	sealing := path + sealingSuffix
	if err := s.copySealed(ctx, path, sealing, key); err != nil {
		_ = removeAll(sealing)
		return err
	}
	// The plain store is closed and its log emptied into it: its log and shared memory
	// must not be taken for the encrypted store's once that takes its name.
	for _, f := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = removeAll(sealing)
			return fmt.Errorf("sealeddb: clear the plain store's log: %w", err)
		}
	}
	if err := os.Rename(path, path+plainSuffix); err != nil {
		_ = removeAll(sealing)
		return fmt.Errorf("sealeddb: set the plain store aside: %w", err)
	}
	if err := os.Rename(sealing, path); err != nil {
		return fmt.Errorf("sealeddb: put the encrypted store in place: %w", err)
	}
	return removeAll(path + plainSuffix)
}

// copySealed writes an encrypted copy of the plain store at path to sealing and checks
// every table came across whole.
func (s Setup) copySealed(ctx context.Context, path, sealing string, key []byte) error {
	plain, err := s.open((&url.URL{Scheme: "file", Opaque: escaped(path), RawQuery: "_pragma=busy_timeout(5000)"}).String())
	if err != nil {
		return fmt.Errorf("sealeddb: open the plain store: %w", err)
	}
	defer func() { _ = plain.Close() }()
	// What the write-ahead log holds goes into the database first, so the copy has it
	// and the log can go: not when another reader kept part of it out (busy), or the
	// plain store set aside would lack those commits.
	var busy, logged, moved int
	if err = plain.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logged, &moved); err != nil {
		return fmt.Errorf("sealeddb: settle the plain store: %w", err)
	}
	if busy != 0 {
		return errors.New("sealeddb: settle the plain store: another process has it open")
	}
	if privErr := Private(sealing); privErr != nil {
		return privErr
	}
	if _, err = plain.ExecContext(ctx, "VACUUM INTO ?", Target(sealing, key)); err != nil {
		return fmt.Errorf("sealeddb: copy the plain store: %w", err)
	}
	want, err := rowCounts(ctx, plain)
	if err != nil {
		return err
	}
	sealed, err := OpenWith(ctx, sealing, key, s)
	if err != nil {
		return err
	}
	defer func() { _ = sealed.Close() }()
	got, err := rowCounts(ctx, sealed)
	if err != nil {
		return err
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("%w: %v, the plain store %v", errCopyDiffers, got, want)
	}
	return nil
}

var errCopyDiffers = errors.New("sealeddb: the encrypted copy does not match the plain store")

// rowCounts is how many rows each table of db holds.
func rowCounts(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	tables, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var n int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`).Scan(&n); err != nil {
			return nil, fmt.Errorf("sealeddb: count %s: %w", table, err)
		}
		counts[table] = n
	}
	return counts, nil
}

// tableNames is db's own tables.
func tableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, fmt.Errorf("sealeddb: list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sealeddb: list tables: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sealeddb: list tables: %w", err)
	}
	return tables, nil
}

// removeAll deletes a database file and its write-ahead log and shared memory.
func removeAll(path string) error {
	var errs []error
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sealeddb: delete %s: %w", path, err)
	}
	return nil
}
