package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A fresh cache already has exactly the declared indexes, so opening changes none: the
// stored text compares equal to baseSchema's.
func TestAFreshCacheNeedsNoIndexChanges(t *testing.T) {
	t.Parallel()
	cache := openTemp(t)
	changed, err := reconcileIndexes(context.Background(), cache.db)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("changed %d indexes on a fresh cache, want 0", changed)
	}
}

// An index whose definition drifted from baseSchema is rebuilt, and one baseSchema no
// longer declares is dropped, the next time the cache is opened.
func TestOpenRebuildsAChangedIndexAndDropsAnUndeclaredOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX reactions_by_sender`,
		`CREATE INDEX reactions_by_sender ON reactions(sender)`, // same name, old columns
		`CREATE INDEX retired_index ON reactions(emoji)`,
	} {
		if _, err = cache.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}

	cache, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	have, err := indexesPresent(ctx, cache.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := have["retired_index"]; ok {
		t.Error("an index baseSchema does not declare survived the open")
	}
	if !sameIndex(have["reactions_by_sender"], baseIndexes()["reactions_by_sender"]) {
		t.Errorf("reactions_by_sender = %q, want the declared definition", have["reactions_by_sender"])
	}
}

// Only the newest copies set aside by rebuilds are kept; the cache itself and other
// files beside it are not touched.
func TestOnlyTheNewestAsideCopiesAreKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache-abc.db")
	names := []string{
		"cache-abc.db", "cache-abc.db-wal", "cache-other.db.pre-schema-20260101-000000",
		"cache-abc.db.pre-schema-20260101-000000",
		"cache-abc.db.pre-schema-20260301-000000",
		"cache-abc.db.pre-schema-20260201-000000",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pruneAside(path, 2)

	for name, kept := range map[string]bool{
		"cache-abc.db": true, "cache-abc.db-wal": true, "cache-other.db.pre-schema-20260101-000000": true,
		"cache-abc.db.pre-schema-20260101-000000": false,
		"cache-abc.db.pre-schema-20260201-000000": true,
		"cache-abc.db.pre-schema-20260301-000000": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != kept {
			t.Errorf("%s exists = %v, want %v", name, exists, kept)
		}
	}
}
