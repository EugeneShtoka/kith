package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// The migration ledger, pinned entry by entry.
var migrationFingerprints = []struct {
	version int    // the user_version this entry stamps
	what    string // what it does, for the failure message
	sha     string // first 12 hex of sha256 over the statement, whitespace collapsed
}{}

func fingerprint(stmt string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(stmt), " ")))
	return hex.EncodeToString(sum[:])[:12]
}

func TestTheMigrationLedgerIsAppendOnly(t *testing.T) {
	t.Parallel()

	if len(migrations) != len(migrationFingerprints) {
		newest := "(none)"
		if len(migrations) > 0 {
			newest = fingerprint(migrations[len(migrations)-1])
		}
		t.Fatalf("migrations has %d entries, the ledger pins %d.\n"+
			"Appending a migration? Add its row to migrationFingerprints — %q for the new one.\n"+
			"Removing one? Don't: a database on disk resumes at its own version and would skip "+
			"whatever moved down to take its place.",
			len(migrations), len(migrationFingerprints), newest)
	}

	for i, want := range migrationFingerprints {
		if got := fingerprint(migrations[i]); got != want.sha {
			t.Errorf("migrations[%d] stamps v%d and should be %s, but its SQL fingerprints as %s "+
				"(pinned: %s).\nA shipped migration was edited, or an entry was inserted rather "+
				"than appended. Either way every cache already past v%d never learns.",
				i, want.version, want.what, got, want.sha, want.version)
		}
	}
}

// An index the file has lost comes back on the next open, at the current version.
func TestABaseIndexComesBackOnOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	const victim = "reactions_by_sender"

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, dropErr := first.db.ExecContext(ctx, "DROP INDEX "+victim); dropErr != nil {
		t.Fatalf("drop %s: %v", victim, dropErr)
	}
	if got := hasIndex(t, first, victim); got {
		t.Fatalf("%s still present after DROP; the test proves nothing", victim)
	}
	version, err := userVersion(ctx, first.db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if want := schemaVersion + len(migrations); version != want {
		t.Fatalf("seeded at version %d, want the current %d — the migration path would "+
			"mask what this test is about", version, want)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = second.Close() }()

	if !hasIndex(t, second, victim) {
		t.Errorf("%s did not come back; a cache that loses an index keeps scanning for ever", victim)
	}
	if rebuilt := second.Rebuilt(); rebuilt {
		t.Error("the cache was rebuilt to restore an index — that costs every cached message " +
			"to recreate something that holds no data")
	}
}

// The parser reads statements, not prose.
func TestBaseIndexesFindsExactlyTheDeclaredOnes(t *testing.T) {
	ctx := context.Background()
	cache := openTemp(t)

	have, err := indexesPresent(ctx, cache.db)
	if err != nil {
		t.Fatalf("indexesPresent: %v", err)
	}
	parsed := baseIndexes()

	for name := range parsed {
		if _, ok := have[name]; !ok {
			t.Errorf("baseIndexes() claims %q, which a fresh cache does not have — the parser "+
				"read prose as SQL", name)
		}
	}
	for name := range have {
		if _, ok := parsed[name]; !ok {
			t.Errorf("a fresh cache has index %q and baseIndexes() missed it, so it would never "+
				"be restored", name)
		}
	}
	if len(parsed) == 0 {
		t.Fatal("baseIndexes() found nothing at all")
	}
}

func hasIndex(t *testing.T, cache *Cache, name string) bool {
	t.Helper()
	have, err := indexesPresent(context.Background(), cache.db)
	if err != nil {
		t.Fatalf("indexesPresent: %v", err)
	}
	_, ok := have[name]
	return ok
}
