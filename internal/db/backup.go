package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/ncruces/go-sqlite3/driver"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A backup carries the cache as a SQLite file encrypted under a key of its own.
// Snapshot writes one from a cache the daemon may be writing, Adopt checks one and
// makes it the cache file at a path under that machine's key, and Settle folds a
// closed cache's write-ahead log into its file so it can be moved as one. All three
// open files through connect, so how a cache is opened is decided in one place, and no
// copy of the history is ever written in the clear.

// ErrNewerCache is a backup made by a newer kith than this one: opening it would
// rebuild it, setting the history aside.
var ErrNewerCache = errors.New("the backup's cache is from a newer kith; update kith, then restore it")

// ErrNotACache is a file that is not a cache this kith can read.
var ErrNotACache = errors.New("not a kith cache this version can read")

// connect opens the cache file at path, encrypted under key: as the cache, or, with
// readOnly, only for reading beside a daemon that may be writing it. A key that does
// not open it is ErrWrongKey. Every cache file is opened here.
func connect(ctx context.Context, path string, key []byte, readOnly bool) (*sql.DB, error) {
	s := setup
	if readOnly {
		s = sealeddb.Setup{Init: setup.Init, ReadOnly: true}
	}
	sqlDB, err := sealeddb.OpenWith(ctx, path, key, s)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	return sqlDB, nil
}

// connectPlain opens a plain cache file at path only for reading: a backup made before
// caches were encrypted.
func connectPlain(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	sqlDB, err := driver.Open(u.String(), setup.Init)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	return sqlDB, nil
}

// Snapshot writes a consistent, self-contained copy of the cache at path (under key)
// to dst under dstKey, a file that must not exist or be empty, and checks the copy.
// The cache is only read, so the daemon may keep running.
func Snapshot(ctx context.Context, path string, key []byte, dst string, dstKey []byte) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("db: no cache to copy: %w", err)
	}
	sqlDB, err := connect(ctx, path, key, true)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(dst, dstKey)); err != nil {
		return fmt.Errorf("db: copy %s: %w", path, err)
	}
	if err := syncFile(dst); err != nil {
		return err
	}
	return checkWritten(ctx, dst, dstKey)
}

// Adopt checks the copy at src (intact, and a schema this kith opens without
// rebuilding) and writes it as a cache file at dst under dstKey, which must not exist
// or be empty, then checks what it wrote. src is under srcKey, or plain for a nil
// srcKey (a backup made before caches were encrypted); it is only read.
func Adopt(ctx context.Context, src string, srcKey []byte, dst string, dstKey []byte) error {
	var sqlDB *sql.DB
	var err error
	if srcKey == nil {
		sqlDB, err = connectPlain(src)
	} else {
		sqlDB, err = connect(ctx, src, srcKey, true)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotACache, err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := restorable(ctx, sqlDB); err != nil {
		return err
	}
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(dst, dstKey)); err != nil {
		return fmt.Errorf("db: write the cache to %s: %w", dst, err)
	}
	if err := syncFile(dst); err != nil {
		return err
	}
	return checkWritten(ctx, dst, dstKey)
}

// checkWritten reports whether the copy just written at path opens under key, whole.
func checkWritten(ctx context.Context, path string, key []byte) error {
	sqlDB, err := connect(ctx, path, key, true)
	if err != nil {
		return fmt.Errorf("db: the copy at %s: %w", path, err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := intact(ctx, sqlDB); err != nil {
		return fmt.Errorf("db: the copy at %s: %w", path, err)
	}
	return nil
}

// intact reports why the open file is damaged, or nil.
func intact(ctx context.Context, sqlDB *sql.DB) error {
	var check string
	if err := sqlDB.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&check); err != nil {
		return fmt.Errorf("%w: %w", ErrNotACache, err)
	}
	if check != "ok" {
		return fmt.Errorf("%w: damaged: %s", ErrNotACache, check)
	}
	return nil
}

// restorable reports why the open file is not a cache to restore: damaged, from a
// newer kith, older than any this kith migrates, or not the shape its version claims.
func restorable(ctx context.Context, sqlDB *sql.DB) error {
	if err := intact(ctx, sqlDB); err != nil {
		return err
	}
	version, err := userVersion(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotACache, err)
	}
	current := schemaVersion + len(currentSchema.migrations)
	switch {
	case version > current:
		return fmt.Errorf("%w (schema %d; this kith reads up to %d)", ErrNewerCache, version, current)
	case version < schemaVersion:
		return fmt.Errorf("%w (schema %d)", ErrNotACache, version)
	case version == current:
		ok, err := shapeMatches(ctx, sqlDB, currentSchema)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNotACache, err)
		}
		if !ok {
			return fmt.Errorf("%w: its tables are not the ones schema %d has", ErrNotACache, version)
		}
	}
	return nil
}

// Settle checkpoints the closed cache at path, under key, so its file holds every
// commit and the write-ahead log is empty. Only call it with no daemon running on the
// cache.
func Settle(ctx context.Context, path string, key []byte) error {
	sqlDB, err := connect(ctx, path, key, false)
	if err != nil {
		return err
	}
	var busy, logged, moved int
	err = sqlDB.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logged, &moved)
	closeErr := sqlDB.Close()
	switch {
	case err != nil:
		return fmt.Errorf("db: checkpoint %s: %w", path, err)
	case busy != 0:
		return fmt.Errorf("db: checkpoint %s: the cache is in use", path)
	case closeErr != nil:
		return fmt.Errorf("db: close %s: %w", path, closeErr)
	}
	return nil
}

// syncFile flushes the file at path to disk.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- a path the caller chose for its copy
	if err != nil {
		return fmt.Errorf("db: open %s to sync: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("db: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("db: close %s: %w", path, err)
	}
	return nil
}
