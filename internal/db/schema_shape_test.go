package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// withoutTable is schema with one CREATE statement (up to its ending) taken out.
func withoutTable(t *testing.T, schema, start, end string) string {
	t.Helper()
	i := strings.Index(schema, start)
	if i < 0 {
		t.Fatalf("no %q in the schema", start)
	}
	j := strings.Index(schema[i:], end)
	return schema[:i] + schema[i+j+len(end):]
}

// seedCache writes a cache file with the given schema and version, and one row.
func seedCache(t *testing.T, schema string, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(context.Background(), schema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `INSERT INTO rooms(id) VALUES('!a:x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	return path
}

// A cache stamped with the current number but made by older code (the fold kept
// version 1, so a cache stamped 1 before it lacks message_edit and message_tombstone)
// is rebuilt, a copy set aside, instead of failing to open.
func TestACacheOfTheCurrentNumberButAnotherShapeIsRebuilt(t *testing.T) {
	t.Parallel()
	preFold := withoutTable(t, baseSchema, "CREATE TABLE message_edit (", ") STRICT, WITHOUT ROWID;")
	preFold = withoutTable(t, preFold, "CREATE INDEX message_edit_by_revision", ";")
	preFold = withoutTable(t, preFold, "CREATE TABLE message_tombstone (", ") STRICT, WITHOUT ROWID;")
	path := seedCache(t, preFold, schemaVersion+len(migrations))
	c, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open = %v, want the cache rebuilt", err)
	}
	defer func() { _ = c.Close() }()
	if !c.Rebuilt() {
		t.Fatal("a cache without message_edit opened as current")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	var aside bool
	for _, e := range entries {
		aside = aside || strings.Contains(e.Name(), asideInfix)
	}
	if !aside {
		t.Error("no copy of the old cache was set aside")
	}
	if ok, err := shapeMatches(context.Background(), c.db); !ok || err != nil {
		t.Errorf("after the rebuild the shape matches = %v, %v", ok, err)
	}
}

// A missing table that no index names is caught by the shape alone: the index
// reconcile would not notice it.
func TestAMissingTableWithoutAnIndexIsRebuilt(t *testing.T) {
	t.Parallel()
	schema := withoutTable(t, baseSchema, "CREATE TABLE message_tombstone (", ") STRICT, WITHOUT ROWID;")
	if strings.Contains(schema, "message_tombstone") {
		t.Fatal("message_tombstone is named elsewhere in the schema: pick another table")
	}
	c, err := Open(context.Background(), seedCache(t, schema, schemaVersion+len(migrations)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if !c.Rebuilt() {
		t.Fatal("a cache without message_tombstone opened as current")
	}
}

// Column order and SQL text are not the shape: a table whose columns sit in another
// order, or one whose last column was added by a hand ALTER (the live cache's
// room_unread), opens as it is.
func TestTheShapeCheckIgnoresColumnOrderAndText(t *testing.T) {
	t.Parallel()
	const marked = "\tmarked        INTEGER NOT NULL DEFAULT 0,\n\tread_ts_ms    INTEGER NOT NULL DEFAULT 0\n"
	if !strings.Contains(baseSchema, marked) {
		t.Fatal("room_unread changed: update this test")
	}
	swapped := strings.Replace(baseSchema, marked,
		"\tread_ts_ms    INTEGER NOT NULL DEFAULT 0,\n\tmarked        INTEGER NOT NULL DEFAULT 0\n", 1)
	altered := strings.Replace(baseSchema, marked, "\tmarked        INTEGER NOT NULL DEFAULT 0\n", 1) +
		";\nALTER TABLE room_unread ADD COLUMN read_ts_ms INTEGER NOT NULL DEFAULT 0;"
	for name, schema := range map[string]string{"swapped": swapped, "altered": altered} {
		path := seedCache(t, schema, schemaVersion+len(migrations))
		c, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Rebuilt() {
			t.Errorf("%s: a cache of the right shape was rebuilt", name)
		}
		_ = c.Close()
	}
}

// A current, well-shaped cache whose index reconcile fails for a passing reason (another
// writer holds the lock past busy_timeout, a full disk) is not rebuilt: that would drop
// what only the cache holds, the drafts first. The error is reported instead.
func TestAFailedIndexReconcileIsAnErrorNotARebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := cache.ReplaceDraft(ctx, domain.StoredDraft{RoomID: "!a:x", Body: "half a thought", Updated: time.Now()}, domain.StoredDraft{}); werr != nil {
		t.Fatal(werr)
	}
	// An index this binary declares is missing (an older binary, or one just added).
	if _, derr := cache.db.ExecContext(ctx, "DROP INDEX messages_recent"); derr != nil {
		t.Fatal(derr)
	}
	_ = cache.Close()

	other, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.SetMaxOpenConns(1)
	tx, err := other.BeginTx(ctx, nil) // _txlock=immediate: takes the write lock
	if err != nil {
		t.Fatal(err)
	}
	if _, ierr := tx.ExecContext(ctx, "INSERT INTO reaction_refusals(protocol, emoji) VALUES('x','y')"); ierr != nil {
		t.Fatal(ierr)
	}
	go func() { time.Sleep(5500 * time.Millisecond); _ = tx.Commit() }() // past busy_timeout

	again, err := Open(ctx, path)
	if err == nil {
		defer func() { _ = again.Close() }()
		if again.Rebuilt() {
			t.Fatal("a passing lock during the index reconcile rebuilt the cache")
		}
	}
	// Either way, the drafts are still there once the lock is gone.
	time.Sleep(6 * time.Second)
	if again == nil {
		if again, err = Open(ctx, path); err != nil {
			t.Fatalf("Open after the lock was released: %v", err)
		}
		defer func() { _ = again.Close() }()
	}
	if drafts, _ := again.Drafts(ctx); len(drafts) != 1 {
		t.Errorf("%d drafts after the reconcile, want the one only the cache held", len(drafts))
	}
}

// The FTS index is kept by triggers: a cache of the current number and tables but
// without one (or with an older one) searches stale text with no error, so it is the
// wrong shape, and is rebuilt.
func TestACacheMissingAnFTSTriggerIsRebuilt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, change := range map[string]string{
		"dropped": "DROP TRIGGER messages_fts_insert",
		"changed": "DROP TRIGGER messages_fts_delete; CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN SELECT 1; END",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cache.db")
			cache, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, cerr := cache.db.ExecContext(ctx, change); cerr != nil {
				t.Fatal(cerr)
			}
			_ = cache.Close()
			again, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = again.Close() }()
			if !again.Rebuilt() {
				t.Error("a cache whose FTS trigger was " + name + " opened as current")
			}
		})
	}
}
