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

// An unrecognized version, older or newer, is rebuilt.
func TestOpenRebuildsAnyUnrecognizedVersion(t *testing.T) {
	t.Parallel()

	current := schemaVersion + len(migrations)
	for _, version := range []int{0, current + 1, 27, 9999} {
		t.Run(fmt.Sprintf("stamped %d", version), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "cache.db")

			// A foreign table makes the rebuild observable.
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
			stale, herr := hasTable(ctx, cache.db, "from_before")
			if herr != nil {
				t.Fatalf("hasTable() error = %v", herr)
			}
			if stale {
				t.Errorf("from_before survived opening a database stamped %d", version)
			}
			if serr := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "A"}}); serr != nil {
				t.Errorf("SaveRooms() after opening a %d: %v", version, serr)
			}
		})
	}
}

// A failed migration leaves neither its DDL nor its version stamp.
func TestApplyMigrationIsAtomic(t *testing.T) {
	failing := newSchemaPlan([]string{
		`CREATE TABLE half_applied(x INTEGER); THIS IS NOT SQL;`,
	})

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	_, err := open(ctx, path, failing)
	if err == nil {
		t.Fatal("Open() accepted a database whose migration could not be applied")
	}
	if !strings.Contains(err.Error(), "migration") {
		t.Errorf("error %q does not say a migration failed", err)
	}

	// Reopen the same file with no migrations, to inspect it.
	cache, err := open(ctx, path, newSchemaPlan(nil))
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

// Migrations apply once, stamp their version, and do not re-run on reopen.
func TestMigrationsApplyOnceAndStampTheirVersion(t *testing.T) {
	plan := newSchemaPlan([]string{
		`CREATE TABLE added_by_migration_one(x INTEGER NOT NULL) STRICT;`,
		`CREATE INDEX added_by_migration_two ON added_by_migration_one(x);`,
	})

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	cache, err := open(ctx, path, plan)
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

	// CREATE TABLE without IF NOT EXISTS would fail if re-run.
	again, err := open(ctx, path, plan)
	if err != nil {
		t.Fatalf("reopen re-ran the migrations: %v", err)
	}
	if again.Rebuilt() {
		t.Error("reopen rebuilt a cache already at its version and shape")
	}
	if closeErr := again.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
}

// The FTS update trigger is narrowed and ANALYZE has run.
func TestTheBaseSchemaHasTheMeasuredShape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cache := openTemp(t)

	// Narrowed by column and value, or re-saving an unchanged message reindexes it.
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

// A base-version database is migrated forward, not rebuilt: its rows survive.
func TestOpenMigratesTheBaseForwardWithoutRebuilding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	func() {
		cache, err := open(ctx, path, newSchemaPlan(nil))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() { _ = cache.Close() }()
		if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!keep:x", Name: "Keep"}}); err != nil {
			t.Fatalf("SaveRooms() error = %v", err)
		}
		if got, verr := userVersion(ctx, cache.db); verr != nil || got != schemaVersion {
			t.Fatalf("seeded at version %d (err %v), want %d", got, verr, schemaVersion)
		}
	}()

	cache, err := open(ctx, path, newSchemaPlan([]string{`CREATE TABLE added_later(x INTEGER NOT NULL) STRICT;`}))
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
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	if len(rooms) != 1 || rooms[0].ID != mx+"!keep:x" {
		t.Errorf("rooms = %+v, want the seeded !keep:x — the upgrade wiped the cache", rooms)
	}
}

// Rebuilt reports a fresh or unrecognized cache, not a current one.
func TestOpenReportsWhetherItRebuilt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

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

	// A false rebuild would rewind the sync position on every start.
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

// stamp forces a cache's user_version.
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

// The copy set aside before a rebuild keeps rows still in the WAL.
func TestTheCacheSetAsideKeepsRowsLivingInTheWAL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

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

	created := regexp.MustCompile(`(?i)CREATE\s+(?:VIRTUAL\s+)?TABLE\s+IF\s+NOT\s+EXISTS\s+(\w+)`)
	// A table a later migration drops is meant to be missing from the base.
	dropped := map[string]bool{}
	for _, stmt := range migrations {
		for _, match := range regexp.MustCompile(`(?i)DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(\w+)`).FindAllStringSubmatch(stmt, -1) {
			dropped[match[1]] = true
		}
	}
	for _, stmt := range migrations {
		for _, match := range created.FindAllStringSubmatch(stmt, -1) {
			table := match[1]
			if dropped[table] {
				continue
			}
			t.Run(table, func(t *testing.T) {
				fresh := storedSQL(t, openTemp(t), table)
				if fresh == "" {
					t.Fatalf("a migration creates %s and baseSchema does not — a fresh cache "+
						"would never have it", table)
				}

				// A cache that predates the migration: the base with the table removed.
				path := filepath.Join(t.TempDir(), "old.db")
				func() {
					cache, err := open(ctx, path, newSchemaPlan(nil))
					if err != nil {
						t.Fatalf("Open() error = %v", err)
					}
					defer func() { _ = cache.Close() }()
					if _, err := cache.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
						t.Fatalf("drop: %v", err)
					}
				}()

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

// The upgrade chain survives a real close and reopen.
func TestTheUpgradeChainSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	func() {
		cache, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer func() { _ = cache.Close() }()
		if err := cache.SaveRoomUpgrade(ctx, mx+"!new:x", RoomUpgrade{Predecessor: mx + "!old:x"}); err != nil {
			t.Fatalf("SaveRoomUpgrade() error = %v", err)
		}
	}()

	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = cache.Close() }()

	chain, err := cache.RoomChain(ctx, mx+"!new:x")
	if err != nil {
		t.Fatalf("RoomChain() error = %v", err)
	}
	if len(chain) != 2 || chain[1] != mx+"!old:x" {
		t.Errorf("chain after reopen = %v, want [!new:x !old:x]", chain)
	}

	// The room list carries the forward link that badges a dead room.
	rooms, err := cache.Rooms(ctx)
	if err != nil {
		t.Fatalf("Rooms() error = %v", err)
	}
	for _, room := range rooms {
		if room.ID == mx+"!old:x" && room.Replacement != mx+"!new:x" {
			t.Errorf("!old:x Replacement = %q, want !new:x", room.Replacement)
		}
	}
}

// storedSQL returns a table's stored CREATE statement, whitespace collapsed.
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
