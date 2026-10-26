package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ftsSegmentDelete is the statement FTS5 runs for every segment a merge retires.
const ftsSegmentDelete = "DELETE FROM messages_fts_data WHERE id>=? AND id<=?"

// However a table without an index got its row count recorded (a new cache's ANALYZE,
// an older build's, an index rebuilt on open, a backup that copied it), the cache Open
// returns holds none and retires search-index segments by rowid, not by reading the
// whole table.
func TestOpenForgetsFrozenRowCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// before shapes the file between a first Open and the Open under test.
		before func(t *testing.T, c *Cache)
		// restored opens a backup of the file, restored, instead of the file.
		restored bool
	}{
		{name: "a new cache"},
		{name: "a count an older build kept", before: func(t *testing.T, c *Cache) {
			t.Helper()
			if _, err := c.db.ExecContext(ctx,
				"INSERT INTO sqlite_stat1(tbl, idx, stat) VALUES('messages_fts_data', NULL, '2')"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an index rebuilt on open", before: func(t *testing.T, c *Cache) {
			t.Helper()
			if _, err := c.db.ExecContext(ctx, "DROP INDEX messages_by_time"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a closed and reopened cache", before: func(*testing.T, *Cache) {}},
		{name: "a restored backup", restored: true, before: func(t *testing.T, c *Cache) {
			t.Helper()
			if _, err := c.db.ExecContext(ctx,
				"INSERT INTO sqlite_stat1(tbl, idx, stat) VALUES('messages_fts_data', NULL, '2')"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cache.db")
			if tc.before != nil {
				first, err := Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				tc.before(t, first)
				if err := first.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.restored {
				snapshot, restored := filepath.Join(t.TempDir(), "backup.db"), filepath.Join(t.TempDir(), "cache.db")
				if err := Snapshot(ctx, path, snapshot); err != nil {
					t.Fatal(err)
				}
				if err := Adopt(ctx, snapshot, restored); err != nil {
					t.Fatal(err)
				}
				path = restored
			}
			cache, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			var frozen int
			if err := cache.db.QueryRowContext(ctx,
				"SELECT count(*) FROM sqlite_stat1 WHERE idx IS NULL").Scan(&frozen); err != nil {
				t.Fatal(err)
			}
			if frozen != 0 {
				t.Errorf("%d table row counts kept; PRAGMA optimize never refreshes them", frozen)
			}
			if plan := queryPlan(t, cache, ftsSegmentDelete); !strings.Contains(plan, "USING INTEGER PRIMARY KEY") {
				t.Errorf("retiring a search-index segment reads the whole table:\n%s", plan)
			}
		})
	}
}
