// Package sealeddb opens SQLite databases encrypted at rest, for the network stores
// whose contents act as the account: device identity keys, prekeys, sessions and
// sender keys. Every page is encrypted, the write-ahead log too, with a 32-byte key
// kept only in the OS keyring (session.LoadOrCreateStoreKey), so whatever a library
// writes into its store is covered without the library knowing.
//
// It is SQLite compiled to WebAssembly (ncruces/go-sqlite3, under wazero) with the
// Adiantum VFS: pure Go, so every binary still builds without cgo. It sits beside the
// cache's own SQLite driver (modernc), which it does not replace.
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

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed" // the SQLite build it runs
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

// Open opens (creating) the encrypted database at path with key, and checks the key
// opens it: a wrong key is ErrWrongKey, at once rather than at the first query.
func Open(ctx context.Context, path string, key []byte) (*sql.DB, error) {
	if len(key) != KeyLen {
		return nil, errKeyLen
	}
	db, err := driver.Open(dsn(path, key))
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
	return db, nil
}

// dsn is the data source name of the encrypted database at path with key: the
// Adiantum VFS, temporary files kept in memory (so they are never written in the
// clear or under a throwaway key), foreign keys on, the write-ahead log, and a busy
// timeout, as the plain stores had.
func dsn(path string, key []byte) string {
	return (&url.URL{Scheme: "file", Opaque: path, RawQuery: url.Values{
		"vfs":     {"adiantum"},
		"hexkey":  {hex.EncodeToString(key)},
		"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "temp_store(memory)"},
	}.Encode()}).String()
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
	if len(key) != KeyLen {
		return nil, errKeyLen
	}
	if err := resume(path); err != nil {
		return nil, err
	}
	plain, err := isPlain(path)
	if err != nil {
		return nil, err
	}
	if plain {
		if err := migrate(ctx, path, key); err != nil {
			return nil, err
		}
	}
	return Open(ctx, path, key)
}

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

// isPlain reports whether path holds a plain SQLite database.
func isPlain(path string) (bool, error) {
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
func migrate(ctx context.Context, path string, key []byte) error {
	sealing := path + sealingSuffix
	if err := copySealed(ctx, path, sealing, key); err != nil {
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
func copySealed(ctx context.Context, path, sealing string, key []byte) error {
	plain, err := driver.Open((&url.URL{Scheme: "file", Opaque: path}).String())
	if err != nil {
		return fmt.Errorf("sealeddb: open the plain store: %w", err)
	}
	defer func() { _ = plain.Close() }()
	// What the write-ahead log holds goes into the database first, so the copy has it.
	if _, err = plain.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("sealeddb: settle the plain store: %w", err)
	}
	if _, err = plain.ExecContext(ctx, "VACUUM INTO ?", dsn(sealing, key)); err != nil {
		return fmt.Errorf("sealeddb: copy the plain store: %w", err)
	}
	want, err := rowCounts(ctx, plain)
	if err != nil {
		return err
	}
	sealed, err := Open(ctx, sealing, key)
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
