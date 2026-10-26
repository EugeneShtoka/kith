// Package db is kith's local SQLite client cache (pure-Go modernc.org/sqlite),
// returning domain types. mautrix-go owns the separate crypto/state stores.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"time"

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
	// hidden is who the muted tags hide (see UseHidden).
	hidden hiddenState
	// arrived hears each room that gained messages (see OnArrived); nil is silent.
	arrived func(domain.RoomID, time.Time)
}

// OnArrived has fn called after each write that cached messages a room did not have,
// live or backfilled, with the time of the oldest of them. It runs on the writer's
// goroutine, after the commit, so it must return at once. Call before use.
func (c *Cache) OnArrived(fn func(room domain.RoomID, since time.Time)) { c.arrived = fn }

// UseKeep sets how many newest messages each room keeps (negative: all). It is
// called before each write's transaction, so it must not write to the cache. Call
// before use; without it every message is kept.
func (c *Cache) UseKeep(keep func(domain.RoomID) int) { c.keep = keep }

// MessagesKept is how many messages a room keeps; negative is every one.
func (c *Cache) MessagesKept(roomID domain.RoomID) int {
	if c.keep == nil {
		return -1
	}
	return c.keep(roomID)
}

// UseSelves sets every ID that is this person (accounts and bridge puppets), so a
// trim keeps their reactions, which rank reaction emoji. Called before each write's
// transaction. Call before use; without it a trim prunes no reactions.
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

// UseLogger sets where partly read rows are reported. Call before use; nil is silent.
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

// Rebuilt reports whether Open recreated the cache. The sync position lives in the
// crypto store and must then be reset, or the discarded history never returns.
func (c *Cache) Rebuilt() bool { return c.rebuilt }

// dsn is the cache's connection string. Pragmas go here because they are per
// connection; _txlock=immediate avoids SQLITE_BUSY_SNAPSHOT against a second opener.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// NORMAL may lose the last commits on power loss, which a cache refetches; FULL
	// would fsync every sync batch.
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	// Escaped so a space or "?" in the path is not read as query.
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	return u.String()
}

// Open opens (creating if needed) the cache at path and brings its schema up to
// date. One connection serializes access.
func Open(ctx context.Context, path string) (*Cache, error) {
	return open(ctx, path, currentSchema)
}

// open is Open with the schema to bring the cache to.
func open(ctx context.Context, path string, plan schemaPlan) (*Cache, error) {
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	state, perr := prepare(ctx, sqlDB, path, plan)
	if perr != nil {
		_ = sqlDB.Close()
		return nil, perr
	}
	return &Cache{db: sqlDB, rebuilt: state.rebuilt, asideErr: state.asideErr}, nil
}

// Close runs PRAGMA optimize and closes the handle.
func (c *Cache) Close() error {
	// Best effort: only planner statistics.
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
		// Everything else cascades from rooms or spaces except reaction_refusals and
		// person_verdicts.
		for _, stmt := range []string{
			"DELETE FROM rooms",
			"DELETE FROM spaces",
			"DELETE FROM reaction_refusals",
			"DELETE FROM person_verdicts",
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
