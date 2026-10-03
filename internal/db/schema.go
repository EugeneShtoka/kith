package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The database is a cache: every row is a copy of something the homeserver has.
// A version this binary does not recognize (either direction) is set aside and
// rebuilt; versions between the base and current are migrated forward. Every
// per-room table references rooms(id) ON DELETE CASCADE (foreign_keys=ON via DSN).

// schemaVersion is the base schema version stamped in PRAGMA user_version.
const schemaVersion = 1

// baseSchema is the whole database; referenced tables come first.
const baseSchema = `
CREATE TABLE rooms (
	id         TEXT    NOT NULL PRIMARY KEY,
	name       TEXT    NOT NULL DEFAULT '',
	is_direct  INTEGER NOT NULL DEFAULT 0,
	membership TEXT    NOT NULL DEFAULT 'join',
	invited_by TEXT    NOT NULL DEFAULT '',
	heroes     TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX rooms_by_membership ON rooms(membership);

-- Side tables for optional per-row facts: a later migration can CREATE one safely,
-- but ALTER TABLE ADD COLUMN has no IF NOT EXISTS.
CREATE TABLE room_topics (
	room_id TEXT NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	topic   TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

-- Local mirror of org.kith.starred account data, so stars survive a rebuild.
CREATE TABLE starred (
	room_id  TEXT    NOT NULL,
	event_id TEXT    NOT NULL,
	at_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id) REFERENCES rooms(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- Local mirror of org.kith.spam account data: rule by number, filter by name.
CREATE TABLE spam_rooms (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	rule    INTEGER NOT NULL,
	filter  TEXT    NOT NULL DEFAULT '',
	at_ms   INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

-- Rooms released from Spam; a room is here or in spam_rooms, never both. Kept
-- because a release outranks every rule.
CREATE TABLE spam_released (
	room_id TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	at_ms   INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE spaces (
	id     TEXT NOT NULL PRIMARY KEY,
	name   TEXT NOT NULL DEFAULT '',
	bridge TEXT NOT NULL DEFAULT '',
	keeper TEXT NOT NULL DEFAULT ''
) STRICT;

-- room_id is not a foreign key: SaveSpaces and SaveRooms run concurrently, and a
-- spaces refresh landing first would fail the whole transaction.
CREATE TABLE space_children (
	space_id TEXT    NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	room_id  TEXT    NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (space_id, room_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX space_children_in_order ON space_children(space_id, position);

-- space_id is not a foreign key: '' records "no canonical parent" (so it is not
-- re-asked), and SQLite exempts only NULL from FK checks.
CREATE TABLE room_parents (
	room_id     TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	space_id    TEXT    NOT NULL DEFAULT '',
	resolved_ms INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX room_parents_by_space ON room_parents(space_id) WHERE space_id <> '';

-- Tombstone/predecessor links. Its own table because SaveRooms is a whole-list
-- snapshot that would blank extra rooms columns. checked_ms records that we looked.
CREATE TABLE room_upgrades (
	room_id     TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	replacement TEXT    NOT NULL DEFAULT '',
	predecessor TEXT    NOT NULL DEFAULT '',
	checked_ms  INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE messages (
	room_id     TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id    TEXT    NOT NULL,
	sender      TEXT    NOT NULL DEFAULT '',
	sender_name TEXT    NOT NULL DEFAULT '',
	body        TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	reply_to    TEXT    NOT NULL DEFAULT '',
	thread_root TEXT    NOT NULL DEFAULT '',
	redacted    INTEGER NOT NULL DEFAULT 0,
	edited      INTEGER NOT NULL DEFAULT 0,
	mentioned   INTEGER NOT NULL DEFAULT 0,
	emote       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id)
) STRICT;
CREATE INDEX messages_by_time   ON messages(room_id, ts_ms, event_id);
CREATE INDEX messages_in_thread ON messages(room_id, thread_root, ts_ms, event_id);
CREATE INDEX messages_by_sender ON messages(sender, ts_ms);
CREATE INDEX messages_by_room_sender ON messages(room_id, sender, thread_root, ts_ms);
CREATE INDEX messages_naming_me ON messages(ts_ms DESC) WHERE mentioned = 1;
CREATE INDEX messages_recent   ON messages(ts_ms DESC);
CREATE INDEX messages_threaded ON messages(room_id, thread_root, ts_ms) WHERE thread_root <> '';

CREATE TABLE message_media (
	room_id   TEXT    NOT NULL,
	event_id  TEXT    NOT NULL,
	kind      TEXT    NOT NULL DEFAULT '',
	name      TEXT    NOT NULL DEFAULT '',
	mime      TEXT    NOT NULL DEFAULT '',
	width     INTEGER NOT NULL DEFAULT 0,
	height    INTEGER NOT NULL DEFAULT 0,
	size      INTEGER NOT NULL DEFAULT 0,
	mxc       TEXT    NOT NULL DEFAULT '',
	file_json TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE message_redaction (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	by       TEXT NOT NULL DEFAULT '',
	reason   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE message_html (
	room_id  TEXT NOT NULL,
	event_id TEXT NOT NULL,
	html     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

-- The edit a message's body and formatting come from. Edits arrive in any order
-- (backfill runs newest page first), and only a newer one may replace it.
CREATE TABLE message_edit (
	room_id     TEXT    NOT NULL,
	event_id    TEXT    NOT NULL,
	revision_id TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
-- A redaction names the edit event, not the message it shows on (EditShownBy).
CREATE INDEX message_edit_by_revision ON message_edit(room_id, revision_id);

-- A redaction of a message not cached yet (ts_ms is when it was deleted): a copy of
-- it arriving later (an edit, which servers do not redact, or a page) is saved as
-- deleted. Dropped once applied, and by trim once older than what the room keeps.
CREATE TABLE message_tombstone (
	room_id  TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT    NOT NULL,
	by       TEXT    NOT NULL DEFAULT '',
	reason   TEXT    NOT NULL DEFAULT '',
	ts_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;

-- Every version of a message, keyed by the event that carried it. Filled only while
-- [display.deleted] keep is on.
CREATE TABLE message_revisions (
	room_id     TEXT    NOT NULL,
	event_id    TEXT    NOT NULL,
	revision_id TEXT    NOT NULL,
	body        TEXT    NOT NULL DEFAULT '',
	html        TEXT    NOT NULL DEFAULT '',
	ts_ms       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, event_id, revision_id),
	FOREIGN KEY (room_id, event_id) REFERENCES messages(room_id, event_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
-- A room's newest edit, asked on every mark-read (NewestTS); the key alone walks
-- every revision in the room.
CREATE INDEX message_revisions_by_time ON message_revisions(room_id, ts_ms);

CREATE VIRTUAL TABLE messages_fts USING fts5(
	body,
	content='messages',
	content_rowid='rowid',
	tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER messages_fts_insert AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, body) VALUES (new.rowid, new.body);
END;
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, body) VALUES('delete', old.rowid, old.body);
END;
-- OF body + WHEN: sync re-delivers unchanged messages constantly.
CREATE TRIGGER messages_fts_update AFTER UPDATE OF body ON messages
	WHEN old.body IS NOT new.body BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, body) VALUES('delete', old.rowid, old.body);
	INSERT INTO messages_fts(rowid, body) VALUES (new.rowid, new.body);
END;


-- Persisted drafts, including ones an agent wrote while the client was closed.
CREATE TABLE drafts (
	room_id    TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	body       TEXT    NOT NULL DEFAULT '',
	caret      INTEGER NOT NULL DEFAULT 0,
	mentions   TEXT    NOT NULL DEFAULT '',
	reply_to   TEXT    NOT NULL DEFAULT '',
	editing    TEXT    NOT NULL DEFAULT '',
	edit_saved TEXT    NOT NULL DEFAULT '',
	author     TEXT    NOT NULL DEFAULT '',
	updated_ms INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

-- The thread a draft is written into: a row only for a draft in one.
CREATE TABLE draft_threads (
	room_id     TEXT NOT NULL PRIMARY KEY REFERENCES drafts(room_id) ON DELETE CASCADE,
	thread_root TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE reactions (
	event_id     TEXT NOT NULL PRIMARY KEY,
	room_id      TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	target_event TEXT NOT NULL,
	sender       TEXT NOT NULL DEFAULT '',
	emoji        TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX reactions_in_room  ON reactions(room_id);
CREATE INDEX reactions_by_sender ON reactions(sender, emoji);

CREATE TABLE room_members (
	room_id      TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	user_id      TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX room_members_by_name ON room_members(room_id, display_name);
CREATE INDEX room_members_by_user ON room_members(user_id);

CREATE TABLE room_unread (
	room_id       TEXT    NOT NULL PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
	notifications INTEGER NOT NULL DEFAULT 0,
	highlights    INTEGER NOT NULL DEFAULT 0,
	read_event    TEXT    NOT NULL DEFAULT '',
	marked        INTEGER NOT NULL DEFAULT 0,
	read_ts_ms    INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE thread_read (
	room_id    TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	root_event TEXT    NOT NULL,
	read_event TEXT    NOT NULL DEFAULT '',
	read_ts_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, root_event)
) STRICT, WITHOUT ROWID;

CREATE TABLE mention_usage (
	room_id      TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	user_id      TEXT    NOT NULL,
	count        INTEGER NOT NULL DEFAULT 0,
	last_used_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, user_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE emoji_usage (
	room_id      TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	kind         TEXT    NOT NULL,
	emoji        TEXT    NOT NULL,
	count        INTEGER NOT NULL DEFAULT 0,
	last_used_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (room_id, kind, emoji)
) STRICT, WITHOUT ROWID;
CREATE INDEX emoji_usage_by_kind ON emoji_usage(kind, emoji, count, last_used_ms);

CREATE TABLE sender_slots (
	room_id   TEXT    NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	group_key TEXT    NOT NULL,
	slot      INTEGER NOT NULL,
	PRIMARY KEY (room_id, group_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE quoted_messages (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	sender   TEXT NOT NULL DEFAULT '',
	body     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE reaction_refusals (
	protocol TEXT    NOT NULL,
	emoji    TEXT    NOT NULL,
	at_ms    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (protocol, emoji)
) STRICT, WITHOUT ROWID;
`

// migrations are post-base changes, append only: entry i stamps
// schemaVersion+i+1. When adding one: also update baseSchema, guard with IF NOT
// EXISTS, prefer a side table over ADD COLUMN, and pin it in migrationFingerprints.
// Indexes need no migration (see ensureIndexes); removals are new DROP migrations.
// TestAnUpgradedCacheMatchesAFreshOne holds a v1 cache, migrated, to a fresh one.
var migrations = []string{
	// v2: drafts remember their thread.
	`CREATE TABLE IF NOT EXISTS draft_threads (
	room_id     TEXT NOT NULL PRIMARY KEY REFERENCES drafts(room_id) ON DELETE CASCADE,
	thread_root TEXT NOT NULL
) STRICT, WITHOUT ROWID;`,
	// v3: what a reply quotes, for a quoted message the cache does not hold.
	`CREATE TABLE IF NOT EXISTS quoted_messages (
	room_id  TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	sender   TEXT NOT NULL DEFAULT '',
	body     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, event_id)
) STRICT, WITHOUT ROWID;`,
}

// ensureIndexes makes the file's explicit indexes exactly baseSchema's: a missing one
// is created, one whose definition changed is rebuilt, and one baseSchema no longer
// declares is dropped. So an index changed in baseSchema needs no migration: indexes
// hold no data, and reconciling on every open brings a fresh, rebuilt or migrated cache
// to the same set. Every explicit index is declared in baseSchema.
func ensureIndexes(ctx context.Context, sqlDB *sql.DB) error {
	_, err := reconcileIndexes(ctx, sqlDB)
	return err
}

// reconcileIndexes is ensureIndexes, reporting how many indexes it changed.
func reconcileIndexes(ctx context.Context, sqlDB *sql.DB) (int, error) {
	have, err := indexesPresent(ctx, sqlDB)
	if err != nil {
		return 0, err
	}
	want := baseIndexes()
	changed := 0
	for name, stmt := range want {
		if def, ok := have[name]; ok && sameIndex(def, stmt) {
			continue
		} else if ok {
			if _, execErr := sqlDB.ExecContext(ctx, "DROP INDEX "+quoteIdent(name)); execErr != nil {
				return changed, fmt.Errorf("db: drop changed index %s: %w", name, execErr)
			}
		}
		if _, execErr := sqlDB.ExecContext(ctx, stmt); execErr != nil {
			return changed, fmt.Errorf("db: restore index %s: %w", name, execErr)
		}
		changed++
	}
	for name := range have {
		if _, declared := want[name]; declared {
			continue
		}
		if _, execErr := sqlDB.ExecContext(ctx, "DROP INDEX "+quoteIdent(name)); execErr != nil {
			return changed, fmt.Errorf("db: drop undeclared index %s: %w", name, execErr)
		}
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if _, err := sqlDB.ExecContext(ctx, "ANALYZE"); err != nil {
		return changed, fmt.Errorf("db: analyze after changing %d index(es): %w", changed, err)
	}
	return changed, nil
}

// quoteIdent quotes an identifier read from sqlite_master, doubling any quote in it.
// Every value in this file is bound; only these names are spliced into SQL.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sameIndex compares a stored index definition with a declared one. SQLite stores the
// statement as written, less any IF NOT EXISTS; spacing and keyword case are not
// meaningful.
func sameIndex(stored, declared string) bool {
	norm := func(sql string) string {
		fields := strings.Fields(sql)
		out := fields[:0]
		for i := 0; i < len(fields); i++ {
			if i+2 < len(fields) && strings.EqualFold(fields[i], "IF") &&
				strings.EqualFold(fields[i+1], "NOT") && strings.EqualFold(fields[i+2], "EXISTS") {
				i += 2
				continue
			}
			out = append(out, fields[i])
		}
		return strings.Join(out, " ")
	}
	return strings.EqualFold(norm(stored), norm(declared))
}

// indexesPresent maps the file's explicit indexes to their stored SQL (auto-indexes
// have none).
func indexesPresent(ctx context.Context, sqlDB *sql.DB) (map[string]string, error) {
	return collectMap(ctx, sqlDB, "indices",
		`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`,
		func(rows *sql.Rows) (string, string, error) {
			var name, def string
			err := rows.Scan(&name, &def)
			return name, def, err
		})
}

// baseIndexes maps each CREATE [UNIQUE] INDEX in baseSchema to its statement,
// parsed from the const (comments stripped) so there is no second list to forget.
func baseIndexes() map[string]string {
	out := map[string]string{}
	for stmt := range strings.SplitSeq(stripSQLComments(baseSchema), ";") {
		fields := strings.Fields(stmt)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "CREATE") {
			continue
		}
		rest := fields[1:]
		if strings.EqualFold(rest[0], "UNIQUE") {
			rest = rest[1:]
		}
		if len(rest) < 2 || !strings.EqualFold(rest[0], "INDEX") {
			continue
		}
		rest = rest[1:]
		if len(rest) >= 4 && strings.EqualFold(rest[0], "IF") {
			rest = rest[3:]
		}
		out[rest[0]] = strings.TrimSpace(stmt)
	}
	return out
}

// stripSQLComments removes `--` line comments, so prose cannot be read as SQL.
func stripSQLComments(script string) string {
	lines := strings.Split(script, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// prepare brings the database at path to the current schema: current (by number
// and by shape) is a no-op bar ensureIndexes, base..current migrates, anything else is
// copied aside and rebuilt. It reports whether it rebuilt so the caller can reset the sync position.
func prepare(ctx context.Context, sqlDB *sql.DB, path string) (prepared, error) {
	version, err := userVersion(ctx, sqlDB)
	if err != nil {
		return prepared{}, err
	}
	current := schemaVersion + len(migrations)
	if version == current {
		// The number alone is not trusted: a version reused for another shape (a fold),
		// or a cache edited by hand, would otherwise open and fail at the first query.
		ok, serr := shapeMatches(ctx, sqlDB)
		if serr != nil {
			return prepared{}, serr
		}
		if ok {
			// The shape is right, so an index that will not build is a passing failure
			// (the lock, the disk), not a wrong shape: a rebuild would drop what only
			// the cache holds.
			return prepared{}, ensureIndexes(ctx, sqlDB)
		}
		version = -1 // not the shape it claims: rebuild, keeping a copy aside
	}
	var out prepared
	if version < schemaVersion || version > current {
		asideErr, fatal := rebuild(ctx, sqlDB, path)
		if fatal != nil {
			return prepared{}, fatal
		}
		out.asideErr = asideErr
		version = schemaVersion
		out.rebuilt = true
	}
	for i := version - schemaVersion; i < len(migrations); i++ {
		if migrateErr := applyMigration(ctx, sqlDB, i); migrateErr != nil {
			return out, migrateErr
		}
	}
	return out, ensureIndexes(ctx, sqlDB)
}

// columnShape is one column as the shape check compares it. Column order, defaults
// and SQL text are left out: a column added by ALTER sits last and reads differently,
// and is the same column.
type columnShape struct {
	typ     string
	notNull bool
	pk      int
}

// expectedShape is every table this code's schema creates, with its columns, built
// once in an in-memory database from the same statements a rebuild runs.
var expectedShape = sync.OnceValues(func() (map[string]map[string]columnShape, error) {
	ctx := context.Background()
	mem, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("db: open the reference schema: %w", err)
	}
	defer func() { _ = mem.Close() }()
	mem.SetMaxOpenConns(1)
	if _, err := mem.ExecContext(ctx, baseSchema); err != nil {
		return nil, fmt.Errorf("db: build the reference schema: %w", err)
	}
	for i := range migrations {
		if err := applyMigration(ctx, mem, i); err != nil {
			return nil, err
		}
	}
	return shapeOf(ctx, mem)
})

// shapeOf is every table in the database with its columns, and every trigger with
// its statement: the FTS index is kept by triggers, and a cache without them searches
// what it held when they went, with no error anywhere.
func shapeOf(ctx context.Context, sqlDB *sql.DB) (map[string]map[string]columnShape, error) {
	tables, err := tablesIn(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]columnShape, len(tables))
	type column struct {
		name  string
		shape columnShape
	}
	for _, table := range tables {
		list, cerr := collect(ctx, sqlDB, "the columns of "+table,
			`SELECT name, type, "notnull", pk FROM pragma_table_xinfo(?)`,
			func(rows *sql.Rows) (column, error) {
				var c column
				err := rows.Scan(&c.name, &c.shape.typ, &c.shape.notNull, &c.shape.pk)
				return c, err
			}, table)
		if cerr != nil {
			return nil, cerr
		}
		cols := make(map[string]columnShape, len(list))
		for _, c := range list {
			cols[c.name] = c.shape
		}
		out[table] = cols
	}
	triggers, terr := collect(ctx, sqlDB, "the triggers",
		`SELECT name, sql FROM sqlite_master WHERE type = 'trigger'`,
		func(rows *sql.Rows) ([2]string, error) {
			var t [2]string
			err := rows.Scan(&t[0], &t[1])
			return t, err
		})
	if terr != nil {
		return nil, terr
	}
	for _, t := range triggers {
		out["trigger "+t[0]] = map[string]columnShape{"": {typ: strings.Join(strings.Fields(t[1]), " ")}}
	}
	return out, nil
}

// shapeMatches reports whether every table the schema creates is in the database with
// the same columns. Tables it does not create are ignored: leftovers do no harm.
func shapeMatches(ctx context.Context, sqlDB *sql.DB) (bool, error) {
	want, err := expectedShape()
	if err != nil {
		return false, err
	}
	have, err := shapeOf(ctx, sqlDB)
	if err != nil {
		return false, err
	}
	for table, cols := range want {
		if got, ok := have[table]; !ok || !maps.Equal(got, cols) {
			return false, nil
		}
	}
	return true, nil
}

type prepared struct {
	rebuilt bool
	// asideErr is why the old cache could not be copied aside; not fatal.
	asideErr error
}

// rebuild copies the old cache aside (history a re-sync may not reproduce), then
// creates the base schema. A failed copy is reported via asideErr, not fatal.
func rebuild(ctx context.Context, sqlDB *sql.DB, path string) (asideErr, fatal error) {
	tables, err := tablesIn(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	// An empty file (first run) has nothing worth setting aside.
	if path != "" && len(tables) > 0 {
		asideErr = copyAside(ctx, sqlDB, path)
	}
	if dropErr := dropAll(ctx, sqlDB, tables); dropErr != nil {
		return asideErr, dropErr
	}
	// Shrink the file; dropped tables only free pages.
	if _, vacErr := sqlDB.ExecContext(ctx, "VACUUM"); vacErr != nil {
		return asideErr, fmt.Errorf("db: vacuum after rebuild: %w", vacErr)
	}
	// Create and stamp in one transaction. The drops stay outside it because
	// PRAGMA foreign_keys is a no-op inside one; an interrupted drop just rebuilds again.
	tx, txErr := sqlDB.BeginTx(ctx, nil)
	if txErr != nil {
		return asideErr, fmt.Errorf("db: begin rebuild: %w", txErr)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, baseSchema); execErr != nil {
		return asideErr, fmt.Errorf("db: create schema: %w", execErr)
	}
	if stampErr := stampVersion(ctx, tx, schemaVersion); stampErr != nil {
		return asideErr, stampErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return asideErr, fmt.Errorf("db: commit rebuild: %w", commitErr)
	}
	// Without statistics the badge query drives from messages instead of read positions.
	if _, anErr := sqlDB.ExecContext(ctx, "ANALYZE"); anErr != nil {
		return asideErr, fmt.Errorf("db: analyze after rebuild: %w", anErr)
	}
	return asideErr, nil
}

// dropAll drops the named tables (with their indexes, triggers and FTS shadows).
func dropAll(ctx context.Context, sqlDB *sql.DB, tables []string) (err error) {
	if _, ferr := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); ferr != nil {
		return fmt.Errorf("db: suspend foreign keys: %w", ferr)
	}
	// Restored even when ctx is canceled: the pool's one connection would otherwise go
	// on with cascades off.
	defer func() {
		if _, rerr := sqlDB.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON"); rerr != nil {
			err = errors.Join(err, fmt.Errorf("db: restore foreign keys: %w", rerr))
		}
	}()

	for _, name := range tables {
		if _, derr := sqlDB.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteIdent(name)); derr != nil {
			return fmt.Errorf("db: drop %s: %w", name, derr)
		}
	}
	return nil
}

// tablesIn lists every table. Virtual tables come first: dropping one takes its
// shadow tables, which SQLite refuses to drop directly.
func tablesIn(ctx context.Context, sqlDB *sql.DB) ([]string, error) {
	return collect(ctx, sqlDB, "tables",
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' "+
			"ORDER BY (rootpage = 0) DESC, rowid",
		func(rows *sql.Rows) (string, error) {
			var name string
			err := rows.Scan(&name)
			return name, err
		})
}

// copyAside keeps the old cache beside the new one via VACUUM INTO, which is
// WAL-aware (a plain file copy misses the -wal) and refuses an existing file.
func copyAside(ctx context.Context, sqlDB *sql.DB, path string) error {
	aside := fmt.Sprintf("%s%s%s", path, asideInfix, time.Now().Format("20060102-150405"))
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", aside); err != nil {
		return fmt.Errorf("db: copy aside to %s: %w", aside, err)
	}
	pruneAside(path, keptAside)
	return nil
}

// asideInfix marks a copy set aside by a rebuild; keptAside is how many are kept.
// Each is a whole cache, and before this every rebuild added one for good.
const (
	asideInfix = ".pre-schema-"
	keptAside  = 2
)

// pruneAside deletes all but the newest keep copies beside path. The timestamp in the
// name sorts in time order. Best effort: a copy left behind costs only disk.
func pruneAside(path string, keep int) {
	dir, base := filepath.Split(path)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return
	}
	var copies []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+asideInfix) && !e.IsDir() {
			copies = append(copies, e.Name())
		}
	}
	slices.Sort(copies)
	for _, name := range copies[:max(len(copies)-keep, 0)] {
		_ = os.Remove(filepath.Join(dir, name)) // best effort, as above
	}
}

// userVersion reads the stamped schema version; an untouched file reads 0.
func userVersion(ctx context.Context, sqlDB *sql.DB) (int, error) {
	var version int
	if err := sqlDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	return version, nil
}

// stampVersion records the version; user_version takes no bind parameter.
func stampVersion(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, version int,
) error {
	if _, err := exec.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("db: stamp schema version %d: %w", version, err)
	}
	return nil
}

// applyMigration applies one migration and stamps its version in one transaction.
func applyMigration(ctx context.Context, sqlDB *sql.DB, index int) error {
	version := schemaVersion + index + 1
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin migration %d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
		return fmt.Errorf("db: apply migration %d: %w", version, err)
	}
	if err := stampVersion(ctx, tx, version); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit migration %d: %w", version, err)
	}
	return nil
}
