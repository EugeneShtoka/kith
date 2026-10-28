// Package db is kith's local SQLite client cache, encrypted at rest (sealeddb, under a
// key from the OS keyring), returning domain types. mautrix-go owns the separate
// crypto/state stores.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// Cache is a handle to the on-disk client cache.
type Cache struct {
	db *sql.DB
	// path is the cache's file, which Vacuum measures and needs room beside.
	path     string
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

// setup is the cache's connections over sealeddb's (the write-ahead log, a busy
// timeout, foreign keys): synchronous NORMAL, which may lose the last commits on power
// loss, which a cache refetches, where FULL would fsync every sync batch; immediate
// transactions, against SQLITE_BUSY_SNAPSHOT with a second opener; and the full-text
// index's FTS5 on every connection.
var setup = sealeddb.Setup{Pragmas: []string{"synchronous(NORMAL)"}, TxLock: "immediate", Init: fts5.Register}

// ErrWrongKey is a cache key will not open: never answered by a new cache, which would
// throw away history only this cache holds.
var ErrWrongKey = sealeddb.ErrWrongKey

// KeyLen is a cache key's length in bytes.
const KeyLen = sealeddb.KeyLen

// Plain reports whether the cache at path was written before caches were encrypted,
// so Open encrypts it first, once.
func Plain(path string) (bool, error) {
	plain, err := sealeddb.IsPlain(path)
	if err != nil {
		return false, fmt.Errorf("db: %w", err)
	}
	return plain, nil
}

// Open opens (creating if needed) the cache at path, encrypted under key, and brings
// its schema up to date; a plain cache from before is encrypted first, once. One
// connection serializes access.
func Open(ctx context.Context, path string, key []byte) (*Cache, error) {
	return open(ctx, path, key, currentSchema)
}

// open is Open with the schema to bring the cache to.
func open(ctx context.Context, path string, key []byte, plan schemaPlan) (*Cache, error) {
	if err := sealeddb.Seal(ctx, path, key, setup); err != nil {
		return nil, fmt.Errorf("db: encrypt %s: %w", path, err)
	}
	sqlDB, err := connect(ctx, path, key, false)
	if err != nil {
		return nil, err
	}
	state, perr := prepare(ctx, sqlDB, path, key, plan)
	if perr != nil {
		_ = sqlDB.Close()
		return nil, perr
	}
	if forgetFrozenStats(ctx, sqlDB) {
		// The connection read the frozen counts when it opened and keeps them after
		// their rows go: start one that never saw them.
		_ = sqlDB.Close()
		if sqlDB, err = connect(ctx, path, key, false); err != nil {
			return nil, err
		}
	}
	return &Cache{db: sqlDB, path: path, rebuilt: state.rebuilt, asideErr: errors.Join(state.asideErr, sealAside(ctx, path, key))}, nil
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
// repopulates from the homeserver. The crypto store is untouched. The numbering of
// changes starts a new epoch: what a reader holds was emptied with the rest.
func (c *Cache) Clear(ctx context.Context) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		// Everything else cascades from rooms or spaces except reaction_refusals and
		// person_verdicts.
		for _, stmt := range []string{
			"DELETE FROM rooms",
			"DELETE FROM spaces",
			"DELETE FROM reaction_refusals",
			"DELETE FROM person_verdicts",
			"UPDATE change_counter SET epoch = lower(hex(randomblob(16)))",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("db: clear (%s): %w", stmt, err)
			}
		}
		return nil
	})
}

// inTx runs fn in a transaction, committing on success; the messages it changed are
// numbered as it commits.
func (c *Cache) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := numberTouched(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}
