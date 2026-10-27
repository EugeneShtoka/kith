package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// backupRoom is the room the backup tests fill.
const backupRoom = domain.RoomID("!backup:example.org")

// fillCache saves n messages to the cache at path, left open as a running daemon's is.
func fillCache(t *testing.T, path string, n int) *Cache {
	t.Helper()
	cache, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	msgs := make([]domain.Message, n)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID: domain.EventID(fmt.Sprintf("$m%d", i)), RoomID: backupRoom, Sender: "@a:example.org",
			Body: fmt.Sprintf("message %d", i), Timestamp: time.UnixMilli(int64(1_700_000_000_000 + i)),
		}
	}
	if err := cache.SaveMessages(context.Background(), backupRoom, msgs); err != nil {
		t.Fatalf("SaveMessages() error = %v", err)
	}
	return cache
}

// bodiesIn opens the cache at path as the daemon would and returns its messages'
// bodies, failing if opening it rebuilt it.
func bodiesIn(t *testing.T, path string) []string {
	t.Helper()
	cache, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatalf("Open(%s) error = %v", path, err)
	}
	defer func() { _ = cache.Close() }()
	if cache.Rebuilt() {
		t.Fatalf("Open(%s) rebuilt the cache: its history was set aside", path)
	}
	msgs, err := cache.Messages(context.Background(), backupRoom, -1)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].Body
	}
	return out
}

// A snapshot of a cache the daemon has open, its last commits still in the log, holds
// every message, and opens as a cache without a rebuild.
func TestSnapshotCopiesEveryCommitWhileTheCacheIsOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "cache.db")
	fillCache(t, src, 300)
	dst := filepath.Join(dir, "copy.db")
	if err := Snapshot(context.Background(), src, testKey, dst, testKey); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got := bodiesIn(t, dst); len(got) != 300 || got[0] != "message 0" || got[299] != "message 299" {
		t.Fatalf("the copy holds %d messages (%v…); want all 300", len(got), got[:min(3, len(got))])
	}
}

// Adopt takes a snapshot of this kith's schema, or of an older one it migrates, and
// the cache it writes opens without a rebuild.
func TestAdoptTakesACacheThisKithOpens(t *testing.T) {
	t.Parallel()
	for name, plan := range map[string]schemaPlan{
		"current schema":    currentSchema,
		"one schema behind": newSchemaPlan(migrations[:len(migrations)-1]),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			src := filepath.Join(dir, "plain.db")
			old, err := open(context.Background(), src, testKey, plan)
			if err != nil {
				t.Fatalf("open() error = %v", err)
			}
			if err := old.SaveMessages(context.Background(), backupRoom, []domain.Message{{
				ID: "$kept", RoomID: backupRoom, Sender: "@a:example.org", Body: "kept", Timestamp: time.UnixMilli(1),
			}}); err != nil {
				t.Fatalf("SaveMessages() error = %v", err)
			}
			if err := old.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			dst := filepath.Join(dir, "cache.db")
			if err := Adopt(context.Background(), src, testKey, dst, testKey); err != nil {
				t.Fatalf("Adopt() error = %v", err)
			}
			if got := bodiesIn(t, dst); len(got) != 1 || got[0] != "kept" {
				t.Fatalf("the adopted cache holds %v; want [kept]", got)
			}
		})
	}
}

// Every file a restore would lose by opening it is refused, and nothing is written:
// opening it would rebuild it (a newer or unknown schema, the wrong tables) or fail.
func TestAdoptRefusesEveryFileTheDaemonWouldRebuildOrFailOn(t *testing.T) {
	t.Parallel()
	exec := func(stmt string) func(t *testing.T, path string) {
		return func(t *testing.T, path string) {
			t.Helper()
			sqlDB, err := connect(context.Background(), path, testKey, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sqlDB.Close() }()
			if _, err := sqlDB.ExecContext(context.Background(), stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	rewrite := func(change func([]byte) []byte) func(t *testing.T, path string) {
		return func(t *testing.T, path string) {
			t.Helper()
			body, err := os.ReadFile(path) // #nosec G304 -- the test's own file
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, change(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	current := schemaVersion + len(migrations)
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
		want   error
	}{
		{"from a newer kith", exec(fmt.Sprintf("PRAGMA user_version = %d", current+1)), ErrNewerCache},
		{"no schema version", exec("PRAGMA user_version = 0"), ErrNotACache},
		{"a table missing", exec("DROP TABLE drafts"), ErrNotACache},
		{"a column missing", exec("ALTER TABLE drafts DROP COLUMN updated_ms"), ErrNotACache},
		{"cut short", rewrite(func(b []byte) []byte { return b[:len(b)/2] }), ErrNotACache},
		{"pages overwritten", rewrite(func(b []byte) []byte {
			for i := 4096; i < len(b) && i < 4096*6; i++ {
				b[i] = 0xA5
			}
			return b
		}), ErrNotACache},
		{"empty", rewrite(func([]byte) []byte { return nil }), ErrNotACache},
		{"not SQLite", rewrite(func([]byte) []byte { return []byte("not a database at all, just words\n") }), ErrNotACache},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			live := filepath.Join(dir, "live.db")
			fillCache(t, live, 200)
			src := filepath.Join(dir, "plain.db")
			if err := Snapshot(context.Background(), live, testKey, src, testKey); err != nil {
				t.Fatalf("Snapshot() error = %v", err)
			}
			tc.damage(t, src)
			dst := filepath.Join(dir, "cache.db")
			err := Adopt(context.Background(), src, testKey, dst, testKey)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Adopt() error = %v; want %v", err, tc.want)
			}
			if info, statErr := os.Stat(dst); statErr == nil && info.Size() > 0 {
				t.Fatalf("a refused file still wrote %s (%d bytes)", dst, info.Size())
			}
		})
	}
}

// Settle folds a log left by a daemon that did not close cleanly into the file, so the
// file alone, moved without its log, holds every commit.
func TestSettleFoldsTheLogIntoTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")
	cache := fillCache(t, live, 50)
	if _, err := cache.db.ExecContext(context.Background(), "PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	mustSave(t, cache, backupRoom, domain.Message{
		ID: "$late", RoomID: backupRoom, Sender: "@a:example.org", Body: "late", Timestamp: time.UnixMilli(1_800_000_000_000),
	})
	// The files as a killed daemon leaves them: the last commits only in the log.
	crashed := filepath.Join(t.TempDir(), "cache.db")
	for _, suffix := range []string{"", "-wal"} {
		body, err := os.ReadFile(live + suffix) // #nosec G304 -- the test's own file
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(crashed+suffix, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Settle(context.Background(), crashed, testKey); err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	moved := filepath.Join(t.TempDir(), "moved.db")
	if err := os.Rename(crashed, moved); err != nil {
		t.Fatal(err)
	}
	got := bodiesIn(t, moved)
	if len(got) != 51 || got[len(got)-1] != "late" {
		t.Fatalf("the settled file alone holds %d messages, last %q; want 51 ending with the late one", len(got), got[len(got)-1])
	}
}
