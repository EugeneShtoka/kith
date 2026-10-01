package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hasTable reports whether the database has a table by that name.
func hasTable(ctx context.Context, sqlDB *sql.DB, name string) (bool, error) {
	tables, err := tablesIn(ctx, sqlDB)
	if err != nil {
		return false, err
	}
	return slices.Contains(tables, name), nil
}

// withMigrations swaps the package's migration list for the duration of one test.
func withMigrations(t *testing.T, list ...string) {
	t.Helper()
	saved := migrations
	migrations = list
	t.Cleanup(func() { migrations = saved })
}

// One rule decides everything: a version this binary does not understand gets
// rebuilt, in *either* direction.
func TestOpenRebuildsAnyUnrecognizedVersion(t *testing.T) {
	t.Parallel()

	current := schemaVersion + len(migrations)
	for _, version := range []int{0, current + 1, 17, 27, 9999} {
		t.Run(fmt.Sprintf("stamped %d", version), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "cache.db")

			// Build a database at that version, with a table this schema does not have
			// so a rebuild is observable.
			seed, openErr := sql.Open("sqlite", dsn(path))
			if openErr != nil {
				t.Fatalf("open seed: %v", openErr)
			}
			if _, execErr := seed.ExecContext(ctx, "CREATE TABLE from_before(x INTEGER)"); execErr != nil {
				t.Fatalf("seed table: %v", execErr)
			}
			if stampErr := stampVersion(ctx, seed, version); stampErr != nil {
				t.Fatalf("stamp %d: %v", version, stampErr)
			}
			if closeErr := seed.Close(); closeErr != nil {
				t.Fatalf("close seed: %v", closeErr)
			}

			cache, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open() on a database stamped %d: %v", version, err)
			}
			defer func() { _ = cache.Close() }()

			if got, verr := userVersion(ctx, cache.db); verr != nil {
				t.Fatalf("userVersion() error = %v", verr)
			} else if got != current {
				t.Errorf("user_version = %d after opening a %d, want %d", got, version, current)
			}
			// Rebuilt, so the table this schema never had is gone.
			stale, herr := hasTable(ctx, cache.db, "from_before")
			if herr != nil {
				t.Fatalf("hasTable() error = %v", herr)
			}
			if stale {
				t.Errorf("from_before survived opening a database stamped %d", version)
			}
			// Usable either way.
			if serr := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "A"}}); serr != nil {
				t.Errorf("SaveRooms() after opening a %d: %v", version, serr)
			}
		})
	}
}

// A migration that fails must leave nothing behind — not its DDL, and not its
// version number.
func TestApplyMigrationIsAtomic(t *testing.T) {
	withMigrations(t,
		`CREATE TABLE half_applied(x INTEGER); THIS IS NOT SQL;`,
	)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	_, err := Open(ctx, path)
	if err == nil {
		t.Fatal("Open() accepted a database whose migration could not be applied")
	}
	if !strings.Contains(err.Error(), "migration") {
		t.Errorf("error %q does not say a migration failed", err)
	}

	// Reopen with the migration list emptied — the same file, inspected.
	migrations = nil
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	version, err := userVersion(ctx, cache.db)
	if err != nil {
		t.Fatalf("userVersion() error = %v", err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d after a failed migration, want %d — the version "+
			"advanced past work that did not land", version, schemaVersion)
	}
	leftover, err := hasTable(ctx, cache.db, "half_applied")
	if err != nil {
		t.Fatalf("hasTable() error = %v", err)
	}
	if leftover {
		t.Error("the failed migration's first statement was committed")
	}
}

// The append-only path, end to end: a migration applies, stamps its index+1, and a
// reopen is a no-op rather than a re-run.
func TestMigrationsApplyOnceAndStampTheirVersion(t *testing.T) {
	withMigrations(t,
		`CREATE TABLE added_by_migration_one(x INTEGER NOT NULL) STRICT;`,
		`CREATE INDEX added_by_migration_two ON added_by_migration_one(x);`,
	)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	version, err := userVersion(ctx, cache.db)
	if err != nil {
		t.Fatalf("userVersion() error = %v", err)
	}
	if want := schemaVersion + 2; version != want {
		t.Errorf("user_version = %d, want %d (base + two migrations)", version, want)
	}
	for _, table := range []string{"added_by_migration_one"} {
		has, herr := hasTable(ctx, cache.db, table)
		if herr != nil {
			t.Fatalf("hasTable(%s) error = %v", table, herr)
		}
		if !has {
			t.Errorf("migration did not create %s", table)
		}
	}
	if closeErr := cache.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	// Reopening must not re-run them: CREATE TABLE without IF NOT EXISTS would fail
	// the second time, which is the cheap proof that the version gate holds.
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen re-ran the migrations: %v", err)
	}
	if closeErr := again.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
}

// The base schema's measured shape, asserted against a database the package built for
// itself.
func TestTheBaseSchemaHasTheMeasuredShape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	// The trigger has to be narrowed on both axes — the column and the value — or a
	// re-save of an unchanged message still reindexes it.
	var trigger string
	if err := cache.db.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='trigger' AND name='messages_fts_update'",
	).Scan(&trigger); err != nil {
		t.Fatalf("read trigger: %v", err)
	}
	for _, want := range []string{"UPDATE OF body", "old.body IS NOT new.body"} {
		if !strings.Contains(trigger, want) {
			t.Errorf("messages_fts_update lacks %q:\n%s", want, trigger)
		}
	}

	// ANALYZE ran, which is what makes the planner choose the indices above.
	var stats int
	if err := cache.db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE name='sqlite_stat1'").Scan(&stats); err != nil {
		t.Fatalf("look for sqlite_stat1: %v", err)
	}
	if stats == 0 {
		t.Error("no sqlite_stat1: ANALYZE never ran, so the planner has no statistics")
	}
}

// indexNames lists the indices on a table, including the implicit ones.
func indexNames(t *testing.T, cache *Cache, table string) []string {
	t.Helper()
	rows, err := cache.db.QueryContext(context.Background(),
		"SELECT name FROM pragma_index_list(?)", table)
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indices: %v", err)
	}
	return out
}

// A database already on the base is migrated forward, not rebuilt — its rows survive.
// This is the ordinary upgrade, and the one that has to be non-destructive: it is what
// happens to an installed client when a release adds a migration.
func TestOpenMigratesTheBaseForwardWithoutRebuilding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	// A genuine base-only database: open with no migrations declared.
	func() {
		withMigrations(t)
		cache, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() { _ = cache.Close() }()
		if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!keep:x", Name: "Keep"}}); err != nil {
			t.Fatalf("SaveRooms() error = %v", err)
		}
		if got, verr := userVersion(ctx, cache.db); verr != nil || got != schemaVersion {
			t.Fatalf("seeded at version %d (err %v), want %d", got, verr, schemaVersion)
		}
	}()

	// Now with a migration declared, the same file upgrades in place.
	withMigrations(t, `CREATE TABLE added_later(x INTEGER NOT NULL) STRICT;`)
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	if got, verr := userVersion(ctx, cache.db); verr != nil {
		t.Fatalf("userVersion() error = %v", verr)
	} else if want := schemaVersion + 1; got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
	if has, herr := hasTable(ctx, cache.db, "added_later"); herr != nil || !has {
		t.Errorf("migration did not run (has=%v err=%v)", has, herr)
	}
	// The point: nothing was rebuilt, so the row is still there.
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	if len(rooms) != 1 || rooms[0].ID != "!keep:x" {
		t.Errorf("rooms = %+v, want the seeded !keep:x — the upgrade wiped the cache", rooms)
	}
}

// The rebuild flag is what pairs the two halves of a reset.
func TestOpenReportsWhetherItRebuilt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	// A first run creates the file from nothing, which goes through rebuild.
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on a fresh path: %v", err)
	}
	if !cache.Rebuilt() {
		t.Error("Rebuilt() = false on a fresh file, want true — there is no history to resume against")
	}
	if cerr := cache.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// Reopening a current cache must not claim a rebuild: that would rewind the
	// sync position on every start and re-fetch the world each time.
	cache, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if cache.Rebuilt() {
		t.Error("Rebuilt() = true reopening a current cache, want false")
	}
	if cerr := cache.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// A version this binary does not know is set aside and rebuilt, in either
	// direction — and the caller has to hear about it.
	for _, version := range []int{schemaVersion - 1, schemaVersion + len(migrations) + 9} {
		stamp(t, path, version)
		cache, err = Open(ctx, path)
		if err != nil {
			t.Fatalf("Open at version %d: %v", version, err)
		}
		if !cache.Rebuilt() {
			t.Errorf("Rebuilt() = false after opening a version %d cache, want true", version)
		}
		if cerr := cache.Close(); cerr != nil {
			t.Fatalf("Close: %v", cerr)
		}
	}
}

// stamp forces a cache's user_version, so the next Open sees a version it does not
// recognize.
func stamp(t *testing.T, path string, version int) {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open to stamp: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(context.Background(),
		fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("stamp %d: %v", version, err)
	}
}

// The cache set aside before a rebuild is a database that opens and still has the
// rows in it — including when the rows are in the WAL rather than in the file.
func TestTheCacheSetAsideKeepsRowsLivingInTheWAL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	// A cache stamped by a schema this binary has never heard of, so Open rebuilds it.
	old, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	old.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE rooms (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '') STRICT`,
		`INSERT INTO rooms VALUES ('!purged:bridge', 'history the server no longer has')`,
		fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+len(migrations)+500),
	} {
		if _, execErr := old.ExecContext(ctx, stmt); execErr != nil {
			t.Fatalf("seeding %q: %v", stmt, execErr)
		}
	}
	if _, statErr := os.Stat(path + "-wal"); statErr != nil {
		t.Fatalf("expected committed data sitting in a WAL beside the cache: %v", statErr)
	}
	// Deliberately NOT closed before the rebuild.
	t.Cleanup(func() { _ = old.Close() })

	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	if !cache.Rebuilt() {
		t.Fatal("a cache stamped by an unknown schema was not rebuilt")
	}
	if asideErr := cache.AsideFailed(); asideErr != nil {
		t.Fatalf("the previous cache could not be set aside: %v", asideErr)
	}

	asides, err := filepath.Glob(path + ".pre-schema-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(asides) != 1 {
		t.Fatalf("expected exactly one cache set aside, got %v", asides)
	}

	kept, err := sql.Open("sqlite", dsn(asides[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kept.Close() }()
	var name string
	if scanErr := kept.QueryRowContext(ctx,
		`SELECT name FROM rooms WHERE id = '!purged:bridge'`).Scan(&name); scanErr != nil {
		t.Fatalf("the cache set aside does not have the row that was in it: %v", scanErr)
	}
	if name != "history the server no longer has" {
		t.Errorf("kept row = %q, want the seeded name", name)
	}
}

// A rebuild drops the FTS5 table before the shadow tables it owns.
func TestRebuildDropsAnFTSTableBeforeItsShadows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)

	// The real base schema, so the FTS table and its shadows are the ones that ship.
	if _, execErr := sqlDB.ExecContext(ctx, baseSchema); execErr != nil {
		t.Fatal(execErr)
	}

	names, err := tablesIn(ctx, sqlDB)
	if err != nil {
		t.Fatalf("tablesIn() = %v", err)
	}
	parent, shadow := slices.Index(names, "messages_fts"), slices.Index(names, "messages_fts_data")
	if parent < 0 {
		t.Fatalf("messages_fts is not in %v", names)
	}
	if shadow >= 0 && parent > shadow {
		t.Errorf("messages_fts is listed after its shadow table: %v", names)
	}

	// The assertion that matters: the order actually works.
	if dropErr := dropAll(ctx, sqlDB, names); dropErr != nil {
		t.Fatalf("dropAll() = %v", dropErr)
	}
	left, err := tablesIn(ctx, sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("tables survived the drop: %v", left)
	}
}

// A table a migration creates is spelled the same way the base schema spells it.
func TestMigratedTablesAreSpelledLikeTheBase(t *testing.T) {
	ctx := context.Background()

	// Captured before withMigrations swaps the list out from under the test.
	real := append([]string(nil), migrations...)

	created := regexp.MustCompile(`(?i)CREATE\s+(?:VIRTUAL\s+)?TABLE\s+IF\s+NOT\s+EXISTS\s+(\w+)`)
	for _, stmt := range real {
		for _, match := range created.FindAllStringSubmatch(stmt, -1) {
			table := match[1]
			t.Run(table, func(t *testing.T) {
				fresh := storedSQL(t, openTemp(t), table)
				if fresh == "" {
					t.Fatalf("a migration creates %s and baseSchema does not — a fresh cache "+
						"would never have it", table)
				}

				// A cache that predates the migration: the base with the table removed.
				path := filepath.Join(t.TempDir(), "old.db")
				func() {
					withMigrations(t)
					cache, err := Open(ctx, path)
					if err != nil {
						t.Fatalf("Open() error = %v", err)
					}
					defer func() { _ = cache.Close() }()
					if _, err := cache.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
						t.Fatalf("drop: %v", err)
					}
				}()

				withMigrations(t, real...)
				migrated, err := Open(ctx, path)
				if err != nil {
					t.Fatalf("reopen error = %v", err)
				}
				defer func() { _ = migrated.Close() }()

				if got := storedSQL(t, migrated, table); got != fresh {
					t.Errorf("migrated table:\n  %s\nbase table:\n  %s", got, fresh)
				}
			})
		}
	}
}

// The chain survives a real open/close cycle, which the in-memory tests do not
// exercise: the table is WITHOUT ROWID with a text primary key, and a link that
// does not come back is a conversation whose history silently ends at the upgrade.
func TestTheUpgradeChainSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	func() {
		cache, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() { _ = cache.Close() }()
		if err := cache.SaveRoomUpgrade(ctx, "!new:x", RoomUpgrade{Predecessor: "!old:x"}); err != nil {
			t.Fatalf("SaveRoomUpgrade() error = %v", err)
		}
	}()

	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	chain, err := cache.RoomChain(ctx, "!new:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if len(chain) != 2 || chain[1] != "!old:x" {
		t.Errorf("chain after reopen = %v, want [!new:x !old:x]", chain)
	}

	// And the room list carries the forward link, which is what badges a dead room.
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	for _, room := range rooms {
		if room.ID == "!old:x" && room.Replacement != "!new:x" {
			t.Errorf("!old:x Replacement = %q, want !new:x", room.Replacement)
		}
	}
}

// storedSQL returns a table's CREATE statement as SQLite kept it, with runs of
// whitespace collapsed so indentation is not the thing under test.
func storedSQL(t *testing.T, cache *Cache, table string) string {
	t.Helper()
	var stmt string
	row := cache.db.QueryRowContext(context.Background(),
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table)
	switch err := row.Scan(&stmt); {
	case errors.Is(err, sql.ErrNoRows):
		return ""
	case err != nil:
		t.Fatalf("read stored sql for %s: %v", table, err)
	}
	return strings.Join(strings.Fields(stmt), " ")
}
