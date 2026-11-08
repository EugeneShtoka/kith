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
	"net/url"
	"os"

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
