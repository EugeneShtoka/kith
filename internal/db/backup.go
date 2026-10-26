package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// A backup carries the cache as a plain SQLite file. Snapshot writes one from a cache
// the daemon may be writing, Adopt checks one and makes it the cache file at a path,
// and Settle folds a closed cache's write-ahead log into its file so it can be moved
// as one. All three open files through connect, so how a cache is opened is decided
// in one place.

// ErrNewerCache is a backup made by a newer kith than this one: opening it would
// rebuild it, setting the history aside.
var ErrNewerCache = errors.New("the backup's cache is from a newer kith; update kith, then restore it")

// ErrNotACache is a file that is not a cache this kith can read.
var ErrNotACache = errors.New("not a kith cache this version can read")

// connect opens the SQLite file at path: as the cache (WAL, foreign keys), or, with
// readOnly, only for reading beside a daemon that may be writing it.
func connect(path string, readOnly bool) (*sql.DB, error) {
	source := dsn(path)
	if readOnly {
		q := url.Values{}
		q.Set("mode", "ro")
		q.Add("_pragma", "busy_timeout(5000)")
		u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
		source = u.String()
	}
	sqlDB, err := sql.Open("sqlite", source)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	return sqlDB, nil
}

// Snapshot writes a consistent, self-contained copy of the cache at path to dst, a
// file that must not exist or be empty. The cache is only read, so the daemon may
// keep running.
func Snapshot(ctx context.Context, path, dst string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("db: no cache to copy: %w", err)
	}
	sqlDB, err := connect(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("db: copy %s: %w", path, err)
	}
	return syncFile(dst)
}

// Adopt checks the plain copy at src (intact, and a schema this kith opens without
// rebuilding) and writes it as a cache file at dst, which must not exist or be empty.
// src is only read.
func Adopt(ctx context.Context, src, dst string) error {
	sqlDB, err := connect(src, true)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()
	if err := restorable(ctx, sqlDB); err != nil {
		return err
	}
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("db: write the cache to %s: %w", dst, err)
	}
	return syncFile(dst)
}

// restorable reports why the open file is not a cache to restore: damaged, from a
// newer kith, older than any this kith migrates, or not the shape its version claims.
func restorable(ctx context.Context, sqlDB *sql.DB) error {
	var check string
	if err := sqlDB.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&check); err != nil {
		return fmt.Errorf("%w: %w", ErrNotACache, err)
	}
	if check != "ok" {
		return fmt.Errorf("%w: damaged: %s", ErrNotACache, check)
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

// Settle checkpoints the closed cache at path, so its file holds every commit and the
// write-ahead log is empty. Only call it with no daemon running on the cache.
func Settle(ctx context.Context, path string) error {
	sqlDB, err := connect(path, false)
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
