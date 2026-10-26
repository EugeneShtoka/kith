package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Rows that leave the cache (a trim, a migration dropping copies) free their pages,
// which later rows reuse, so the file never shrinks by itself. Vacuum rewrites it
// without them once they are a real share of it: a fifth of the file and 32 MiB, which
// a migration that drops a third of the cache reaches, and a day of trims and edits
// does not.
const (
	vacuumShare   = 5        // free pages at least 1/vacuumShare of the file
	vacuumMinFree = 32 << 20 // and at least this many bytes
)

// ErrVacuumPutOff is a vacuum worth running that could not run now: the disks lack
// room for the rewrite, or another process is reading the cache.
var ErrVacuumPutOff = errors.New("cache vacuum put off")

// Vacuumed is what a Vacuum found and did. Before and After are the file's bytes, Free
// the bytes its free pages held before.
type Vacuumed struct {
	Ran           bool
	Before, After int64
	Free          int64
	Took          time.Duration
}

// Vacuum rewrites the cache without its free pages when they are worth it (see
// vacuumShare), and reports what it found when they are not. tempDir is where SQLite
// writes its temporary files, whose disk needs room for the rewrite too unless the
// cache keeps them in memory (temp_store). The rewrite
// holds the cache's one connection, so every other read and write waits for it rather
// than failing; ctx ending interrupts it, leaving the file as it was.
func (c *Cache) Vacuum(ctx context.Context, tempDir string) (Vacuumed, error) {
	var pageSize, pages, free, tempStore int64
	for pragma, v := range map[string]*int64{
		"page_size": &pageSize, "page_count": &pages, "freelist_count": &free, "temp_store": &tempStore,
	} {
		if err := c.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(v); err != nil {
			return Vacuumed{}, fmt.Errorf("db: read %s: %w", pragma, err)
		}
	}
	found := Vacuumed{Before: pages * pageSize, Free: free * pageSize}
	if !worthVacuum(pageSize, pages, free) {
		return found, nil
	}
	if tempStore == tempStoreMemory {
		tempDir = ""
	}
	if err := roomToVacuum(filepath.Dir(c.path), tempDir, (pages-free)*pageSize); err != nil {
		return found, err
	}
	// The rewrite goes through the write-ahead log, which only empties once no other
	// process reads an older state (a backup copying the cache): checked now, so the
	// log does not sit at the cache's size until it does.
	if busy, err := c.checkpoint(ctx); err != nil {
		return found, err
	} else if busy {
		return found, fmt.Errorf("%w: another process is reading the cache", ErrVacuumPutOff)
	}
	if info, err := os.Stat(filepath.Clean(c.path)); err == nil {
		found.Before = info.Size()
	}
	start := time.Now()
	if _, err := c.db.ExecContext(ctx, "VACUUM"); err != nil {
		return found, fmt.Errorf("db: vacuum: %w", err)
	}
	found.Ran, found.Took = true, time.Since(start)
	// Moves the rewrite into the file, which shrinks to it, and empties the log. A
	// reader that came since leaves both for a later checkpoint.
	if _, err := c.checkpoint(ctx); err != nil {
		return found, err
	}
	if info, err := os.Stat(filepath.Clean(c.path)); err == nil {
		found.After = info.Size()
	}
	return found, nil
}

// worthVacuum reports whether free of pages pages of pageSize bytes are worth a
// rewrite: a share of the file, and enough bytes to matter.
func worthVacuum(pageSize, pages, free int64) bool {
	return free*vacuumShare >= pages && free*pageSize >= vacuumMinFree
}

// checkpoint copies the write-ahead log into the file and empties it; busy when a
// reader of an older state kept it from finishing.
func (c *Cache) checkpoint(ctx context.Context) (busy bool, err error) {
	var blocked, logged, moved int
	if err := c.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&blocked, &logged, &moved); err != nil {
		return false, fmt.Errorf("db: checkpoint: %w", err)
	}
	return blocked != 0, nil
}

// disk is a filesystem: an identity to tell two directories on one apart, and the
// bytes free on it.
type disk struct {
	id   string
	free uint64
}

// tempStoreMemory is PRAGMA temp_store's answer for temporary files kept in memory.
const tempStoreMemory = 2

// roomToVacuum reports, as ErrVacuumPutOff, a rewrite of size bytes the disks cannot
// hold: SQLite builds it as a temporary file in tempDir ("" when it keeps it in
// memory), then writes it into the write-ahead log beside the cache; one disk holding
// both needs room for both. Each disk keeps a tenth of what it takes spare.
func roomToVacuum(cacheDir, tempDir string, size int64) error {
	cacheDisk, err := diskOf(cacheDir)
	if err != nil {
		return fmt.Errorf("db: free space beside the cache: %w", err)
	}
	disks := []disk{cacheDisk}
	if tempDir != "" {
		tempDisk, err := diskOf(tempDir)
		if err != nil {
			return fmt.Errorf("db: free space for SQLite's temporary files: %w", err)
		}
		disks = append(disks, tempDisk)
	}
	return fits(uint64(max(size, 0)), disks...)
}

// fits is roomToVacuum's decision over the disks the rewrite is written to.
func fits(size uint64, disks ...disk) error {
	need := map[string]uint64{}
	free := map[string]uint64{}
	for _, d := range disks {
		need[d.id] += size + size/10
		free[d.id] = d.free
	}
	for id, n := range need {
		if free[id] < n {
			return fmt.Errorf("%w: rewriting it takes %d MiB free where only %d MiB are", ErrVacuumPutOff, n>>20, free[id]>>20)
		}
	}
	return nil
}
