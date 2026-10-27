package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pad gives the cache mb MiB of rows in a table of the test's own; with free it
// deletes them again, so their pages are free pages.
func pad(t *testing.T, c *Cache, table string, mb int, free bool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		"CREATE TABLE " + table + "(b BLOB)",
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
			INSERT INTO %s SELECT randomblob(4000) FROM n`, mb*256, table),
	} {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if free {
		if _, err := c.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatal(err)
		}
	}
}

// saveSome caches n messages in room, numbered from first.
func saveSome(t *testing.T, c *Cache, room domain.RoomID, first, n int) {
	t.Helper()
	msgs := make([]domain.Message, n)
	for i := range msgs {
		at := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).Add(time.Duration(first+i) * time.Second)
		msgs[i] = domain.Message{
			ID: domain.EventID(fmt.Sprintf("$m%d", first+i)), RoomID: room,
			Sender: "matrix:@dana:example.org", Body: fmt.Sprintf("message %d", first+i), Timestamp: at,
		}
	}
	if err := c.SaveMessages(context.Background(), room, msgs); err != nil {
		t.Fatal(err)
	}
}

// countMessages is how many messages room has cached.
func countMessages(t *testing.T, c *Cache, room domain.RoomID) int {
	t.Helper()
	msgs, err := c.Messages(context.Background(), room, -1)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// Free pages a fifth of the file and past 32 MiB are worth a rewrite; fewer, or a
// share too small to matter, are not.
func TestWorthVacuum(t *testing.T) {
	t.Parallel()
	const page = 4096
	mib := func(n int64) int64 { return n << 20 / page }
	for _, tc := range []struct {
		name        string
		pages, free int64
		want        bool
	}{
		{"a cache whose copies were dropped (134 of 471 MiB)", mib(471), mib(134), true},
		{"exactly a fifth, past 32 MiB", mib(200), mib(40), true},
		{"a day of trims (40 of 471 MiB)", mib(471), mib(40), false},
		{"most of a small cache (20 of 30 MiB)", mib(30), mib(20), false},
		{"just under 32 MiB", mib(60), mib(32) - 1, false},
		{"none", mib(471), 0, false},
	} {
		if got := worthVacuum(page, tc.pages, tc.free); got != tc.want {
			t.Errorf("%s: worthVacuum = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A rewrite needs its size, and a tenth spare, on the temporary files' disk and
// again beside the cache: twice on one disk holding both.
func TestFitsCountsADiskHoldingBothTwice(t *testing.T) {
	t.Parallel()
	const size = 100 << 20
	for _, tc := range []struct {
		name        string
		cache, temp disk
		fits        bool
	}{
		{"two disks with room", disk{"a", 111 << 20}, disk{"b", 111 << 20}, true},
		{"the cache's disk short", disk{"a", 109 << 20}, disk{"b", 1 << 30}, false},
		{"the temporary disk short", disk{"a", 1 << 30}, disk{"b", 109 << 20}, false},
		{"one disk with room for both", disk{"a", 221 << 20}, disk{"a", 221 << 20}, true},
		{"one disk with room for one", disk{"a", 150 << 20}, disk{"a", 150 << 20}, false},
	} {
		err := fits(size, tc.cache, tc.temp)
		if tc.fits != (err == nil) {
			t.Errorf("%s: fits = %v", tc.name, err)
		}
		if err != nil && !errors.Is(err, ErrVacuumPutOff) {
			t.Errorf("%s: %v is not a put-off", tc.name, err)
		}
	}
	if err := fits(size, disk{"a", 111 << 20}); err != nil {
		t.Errorf("temporary files in memory, the cache's disk with room: %v", err)
	}
	if err := roomToVacuum(t.TempDir(), t.TempDir(), 1<<20); err != nil {
		t.Errorf("a MiB on this machine's disks: %v", err)
	}
}

// A cache that keeps temporary files in memory needs no room for them on disk: a
// temporary directory that is not there does not hold its vacuum back.
func TestVacuumInMemoryNeedsNoTemporaryDirectory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := openTemp(t)
	if _, err := c.db.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
		t.Fatal(err)
	}
	pad(t, c, "gone", 40, true)
	v, err := c.Vacuum(ctx, filepath.Join(t.TempDir(), "not there"))
	if err != nil || !v.Ran {
		t.Errorf("Vacuum = %+v, %v", v, err)
	}
}

// A cache with many free pages is rewritten without them: the file shrinks, its log
// is emptied, and every message is still there.
func TestVacuumShrinksACacheWithManyFreePages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	const room = domain.RoomID(mx + "!room:example.org")
	saveSome(t, c, room, 0, 500)
	pad(t, c, "gone", 40, true)
	before := fileSize(t, path)

	v, err := c.Vacuum(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Ran || v.Free < 40<<20 {
		t.Fatalf("Vacuum = %+v, want a run over 40 MiB free", v)
	}
	after := fileSize(t, path)
	if after > before-(40<<20) || v.After != after || v.Before < before {
		t.Errorf("file %d → %d bytes, reported %d → %d", before, after, v.Before, v.After)
	}
	if wal := fileSize(t, path+"-wal"); wal != 0 {
		t.Errorf("the log keeps %d bytes", wal)
	}
	if n := countMessages(t, c, room); n != 500 {
		t.Errorf("%d messages after, want 500", n)
	}
	var check string
	if err := c.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Errorf("integrity_check = %q, %v", check, err)
	}
	// The rewrite goes through the cache's encryption: nothing of it is in the clear.
	if left := plainFilesIn(t, filepath.Dir(path)); len(left) > 0 {
		t.Errorf("the vacuum left the cache in the clear: %v", left)
	}
	ownersOnly(t, filepath.Dir(path))
}

// A cache whose free pages are too few to matter is left as it is (the share rule's
// cases are TestWorthVacuum's: a cache big enough to test it takes long to make).
func TestVacuumLeavesACacheWithFewFreePages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pad(t, c, "kept", 1, false)
	pad(t, c, "gone", 20, true)
	if _, err = c.checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	before := fileSize(t, path)
	v, err := c.Vacuum(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if v.Ran || v.Free < 20<<20 {
		t.Errorf("Vacuum = %+v, want 20 MiB free left alone", v)
	}
	if after := fileSize(t, path); after != before {
		t.Errorf("file %d → %d bytes", before, after)
	}
}

// Saves made while a vacuum runs wait for it and are kept.
func TestSavesDuringAVacuumAreKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, err := Open(ctx, filepath.Join(t.TempDir(), "cache.db"), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	const room = domain.RoomID(mx + "!room:example.org")
	pad(t, c, "kept", 20, false)
	pad(t, c, "gone", 40, true)

	var wg sync.WaitGroup
	done := make(chan struct{})
	var v Vacuumed
	var verr error
	wg.Go(func() {
		defer close(done)
		v, verr = c.Vacuum(ctx, t.TempDir())
	})
	saved := 0
	for running := true; running; saved++ {
		select {
		case <-done:
			running = false
		default:
		}
		saveSome(t, c, room, saved, 1)
	}
	wg.Wait()
	if verr != nil || !v.Ran {
		t.Fatalf("Vacuum = %+v, %v", v, verr)
	}
	if n := countMessages(t, c, room); n != saved {
		t.Errorf("%d messages cached, %d saved", n, saved)
	}
}

// While another process reads the cache, a vacuum is put off and changes nothing:
// its rewrite would sit in the log until that reader let go.
func TestVacuumWaitsForAReaderElsewhere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pad(t, c, "gone", 40, true)

	reader, err := connect(ctx, path, testKey, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM rooms").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A write the reader's snapshot predates, so the log has frames it still needs.
	pad(t, c, "after", 1, false)

	before := fileSize(t, path)
	v, err := c.Vacuum(ctx, t.TempDir())
	if !errors.Is(err, ErrVacuumPutOff) || v.Ran {
		t.Fatalf("Vacuum = %+v, %v; want put off", v, err)
	}
	if after := fileSize(t, path); after != before {
		t.Errorf("file %d → %d bytes", before, after)
	}
	_ = tx.Rollback()
	if v, err = c.Vacuum(ctx, t.TempDir()); err != nil || !v.Ran {
		t.Errorf("once the reader is done: Vacuum = %+v, %v", v, err)
	}
}

// A vacuum interrupted by its context ending (kithd stopping) leaves the cache as it
// was, and usable.
func TestAnInterruptedVacuumLeavesTheCacheAsItWas(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	const room = domain.RoomID(mx + "!room:example.org")
	saveSome(t, c, room, 0, 100)
	pad(t, c, "kept", 60, false)
	pad(t, c, "gone", 45, true)
	// Checkpointed first, so the time runs out in the rewrite, not in the checkpoint.
	if _, err := c.checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if v, err := c.Vacuum(ctx, t.TempDir()); err == nil || v.Ran {
		t.Fatalf("Vacuum = %+v, %v; want interrupted", v, err)
	}
	if n := countMessages(t, c, room); n != 100 {
		t.Errorf("%d messages after, want 100", n)
	}
	var check string
	if err := c.db.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Errorf("integrity_check = %q, %v", check, err)
	}
	saveSome(t, c, room, 100, 1)
}
