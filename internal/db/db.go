// Package db is kith's local SQLite client cache (pure-Go modernc.org/sqlite),
// returning domain types. mautrix-go owns the separate crypto/state stores.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Cache is a handle to the on-disk client cache.
type Cache struct {
	db       *sql.DB
	rebuilt  bool
	asideErr error
	// log hears rows the cache had to read around (see UseLogger); nil is silent.
	log *slog.Logger
	// selves is who this person is (see UseSelves); nil until set.
	selves func() []string
	// keep is how many messages a room keeps (see UseKeep); nil keeps every one.
	keep func(domain.RoomID) int
}

// UseKeep sets how many messages each room keeps, the newest; a negative answer keeps
// them all. It is asked at each write, before its transaction, so it must not write
// to the cache. Call before use; without it every message is kept.
func (c *Cache) UseKeep(keep func(domain.RoomID) int) { c.keep = keep }

// MessagesKept is how many messages a room keeps; negative is every one.
func (c *Cache) MessagesKept(roomID domain.RoomID) int {
	if c.keep == nil {
		return -1
	}
	return c.keep(roomID)
}

// UseSelves sets who this person is: every ID on every network that is them (each
// network's own accounts, and the IDs bridges post as for them), so a trim keeps
// their reactions: they are what reaction emoji are ranked from. It is asked at each
// write, before its transaction. Call before use; without it a trim prunes no
// reactions at all.
func (c *Cache) UseSelves(selves func() []string) { c.selves = selves }

// mine is every ID UseSelves says is this person, the empty one left out.
func (c *Cache) mine() []string {
	if c.selves == nil {
		return nil
	}
	var mine []string
	for _, id := range c.selves() {
		if id != "" {
			mine = append(mine, id)
		}
	}
	return mine
}

// UseLogger sets where the cache reports a row it could only partly read (the rest
// is still returned). Call before use; nil keeps the silent default.
func (c *Cache) UseLogger(log *slog.Logger) { c.log = log }

// warn logs a partial read; silent without a logger.
func (c *Cache) warn(ctx context.Context, msg string, err error, attrs ...any) {
	if c.log != nil && err != nil {
		c.log.WarnContext(ctx, msg, append([]any{"err", err}, attrs...)...)
	}
}

// AsideFailed reports why the previous cache could not be copied aside before a
// rebuild, or nil. Not fatal, but callers should surface it.
func (c *Cache) AsideFailed() error { return c.asideErr }

// Rebuilt reports whether Open set the previous cache aside and recreated it. The
// sync position lives in the crypto store, so whoever owns it must reset it then,
// or the discarded history never comes back.
func (c *Cache) Rebuilt() bool { return c.rebuilt }

// dsn is the connection string for the cache at path. Pragmas go in the DSN
// because foreign_keys and busy_timeout are per connection and the pool may open
// new ones. _txlock=immediate avoids SQLITE_BUSY_SNAPSHOT against a second opener.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// NORMAL is durable under WAL except for the last commits before a power loss, which
	// a cache refetches; FULL would fsync every sync batch.
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	// Escaped so a space or "?" in the path is not read as query.
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	return u.String()
}

// Open opens (creating if needed) the cache at path and brings its schema up to
// date. One connection serializes access.
func Open(ctx context.Context, path string) (*Cache, error) {
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	state, perr := prepare(ctx, sqlDB, path)
	if perr != nil {
		_ = sqlDB.Close() // cleanup on the error path; perr is the report
		return nil, perr
	}
	return &Cache{db: sqlDB, rebuilt: state.rebuilt, asideErr: state.asideErr}, nil
}

// Close runs PRAGMA optimize (best effort, keeps planner statistics fresh) and
// closes the handle.
func (c *Cache) Close() error {
	// Only planner statistics: a failure costs query speed, never data.
	_, _ = c.db.ExecContext(context.Background(), "PRAGMA optimize")
	if err := c.db.Close(); err != nil {
		return fmt.Errorf("db: close: %w", err)
	}
	return nil
}

// Clear empties every cached table in one transaction so the next startup
// repopulates from the homeserver. The crypto store is untouched.
func (c *Cache) Clear(ctx context.Context) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		// Everything else cascades from rooms or spaces except reaction_refusals.
		for _, stmt := range []string{
			"DELETE FROM rooms",
			"DELETE FROM spaces",
			"DELETE FROM reaction_refusals",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("db: clear (%s): %w", stmt, err)
			}
		}
		return nil
	})
}

// inTx runs fn in a transaction, committing on success.
func (c *Cache) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}
