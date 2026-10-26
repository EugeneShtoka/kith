package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// sealedRoom is the room the encryption tests fill.
const sealedRoom = domain.RoomID("!sealed:example.org")

// plainCache writes a cache as kith wrote one before caches were encrypted: plain, n
// messages saying "plain <i>", the last late ones only in its write-ahead log when
// killed (a daemon that never closed it).
func plainCache(t *testing.T, path string, n int, killed bool) {
	t.Helper()
	ctx := context.Background()
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: "_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"}
	sqlDB, err := driver.Open(u.String(), setup.Init)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := prepare(ctx, sqlDB, path, nil, currentSchema); err != nil {
		t.Fatal(err)
	}
	c := &Cache{db: sqlDB}
	msgs := make([]domain.Message, n)
	for i := range msgs {
		msgs[i] = domain.Message{
			ID: domain.EventID(fmt.Sprintf("$p%d", i)), RoomID: sealedRoom, Sender: "@a:example.org",
			Body: fmt.Sprintf("plain %d", i), Timestamp: time.UnixMilli(int64(1_700_000_000_000 + i)),
		}
	}
	if !killed {
		if err := c.SaveMessages(ctx, sealedRoom, msgs); err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := c.SaveMessages(ctx, sealedRoom, msgs[:n-5]); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMessages(ctx, sealedRoom, msgs[n-5:]); err != nil {
		t.Fatal(err)
	}
	// The files as a killed daemon leaves them: the last commits only in the log.
	killedAt := path + ".killed"
	for _, suffix := range []string{"", "-wal"} {
		body, err := os.ReadFile(path + suffix) // #nosec G304 -- the test's own file
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(killedAt+suffix, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = sqlDB.Close()
	for _, suffix := range []string{"", "-wal"} {
		if err := os.Rename(killedAt+suffix, path+suffix); err != nil {
			t.Fatal(err)
		}
	}
}

// settlePlain empties the plain cache's log into it and deletes the log.
func settlePlain(t *testing.T, path string) {
	t.Helper()
	sqlDB, err := driver.Open("file:"+path, setup.Init)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

// plainFilesIn names every file in dir that holds a plain SQLite database or log: the
// history in the clear.
func plainFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var plain []string
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		if err != nil || e.IsDir() {
			continue
		}
		head := make([]byte, 16)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		// A database starts "SQLite format 3", a write-ahead log with its magic.
		if bytes.HasPrefix(head[:n], []byte("SQLite format 3")) || bytes.HasPrefix(head[:n], []byte{0x37, 0x7f, 0x06, 0x82}) ||
			bytes.HasPrefix(head[:n], []byte{0x37, 0x7f, 0x06, 0x83}) {
			plain = append(plain, e.Name())
		}
	}
	return plain
}

// sealedHolds checks the cache at path opens under testKey with all n messages, the
// late ones too, that search finds them, that another key does not open it, and that
// nothing beside it is in the clear.
func sealedHolds(t *testing.T, path string, n int) {
	t.Helper()
	ctx := context.Background()
	cache, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	msgs, err := cache.Messages(ctx, sealedRoom, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n || msgs[n-1].Body != fmt.Sprintf("plain %d", n-1) {
		t.Errorf("the encrypted cache holds %d messages; want %d, the last %q", len(msgs), n, fmt.Sprintf("plain %d", n-1))
	}
	hits, err := cache.SearchMessages(ctx, domain.SearchRequest{Filter: domain.SearchFilter{Terms: "plain"}, Rooms: domain.EveryRoom(), Limit: n + 1})
	if err != nil || len(hits) != n {
		t.Errorf("searching the encrypted cache found %d (%v); want %d", len(hits), err, n)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if left := plainFilesIn(t, filepath.Dir(path)); len(left) > 0 {
		t.Errorf("history left in the clear: %v", left)
	}
	other := bytes.Repeat([]byte{8}, len(testKey))
	if _, err := Open(ctx, path, other); !errors.Is(err, ErrWrongKey) {
		t.Errorf("another key on the encrypted cache: %v; want ErrWrongKey", err)
	}
}

// A plain cache is encrypted on its first open however the last kithd left it (closed,
// or killed with commits only in its log) and wherever a migration was cut (a half
// copy, the plain cache set aside before the copy took its name, or after): every
// message is kept and searchable, and no file beside it is left in the clear.
func TestAPlainCacheIsEncryptedWhereverItWasLeft(t *testing.T) {
	t.Parallel()
	const n = 60
	crashes := map[string]func(t *testing.T, path string){
		"no migration yet": func(*testing.T, string) {},
		"a half-written copy": func(t *testing.T, path string) {
			if err := os.WriteFile(path+".sealing", []byte("half a copy"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"the plain cache set aside, the copy not in place": func(t *testing.T, path string) {
			// As a migration sets it aside: its log emptied into it first.
			settlePlain(t, path)
			if err := os.Rename(path, path+".plain"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".sealing", []byte("an unchecked copy"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"the copy in place, the plain cache not yet deleted": func(t *testing.T, path string) {
			copied := filepath.Join(t.TempDir(), "cache.db")
			for _, suffix := range []string{"", "-wal"} {
				if body, err := os.ReadFile(path + suffix); err == nil { // #nosec G304 -- the test's own file
					if err := os.WriteFile(copied+suffix, body, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			c, err := Open(context.Background(), copied, testKey)
			if err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			if err := os.Rename(path, path+".plain"); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path + "-wal")
			if err := os.Rename(copied, path); err != nil {
				t.Fatal(err)
			}
		},
	}
	for _, killed := range []bool{false, true} {
		for name, crash := range crashes {
			t.Run(fmt.Sprintf("%s, killed=%t", name, killed), func(t *testing.T) {
				t.Parallel()
				path := filepath.Join(t.TempDir(), "cache.db")
				plainCache(t, path, n, killed)
				crash(t, path)
				sealedHolds(t, path, n)
			})
		}
	}
}

// Openers racing on a plain cache (a kithd and a kith restart, a script) leave one
// encrypted cache with every message: none deletes the copy another is writing, none
// renames over another, and the plain cache is never lost.
func TestOpenersRacingToEncryptACacheLoseNothing(t *testing.T) {
	t.Parallel()
	const n = 400
	for round := range 5 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cache.db")
			plainCache(t, path, n, round%2 == 1)
			var wg sync.WaitGroup
			errs := make([]error, 4)
			for i := range errs {
				wg.Go(func() {
					c, err := Open(context.Background(), path, testKey)
					if err == nil {
						err = c.Close()
					}
					errs[i] = err
				})
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Errorf("opener %d: %v", i, err)
				}
			}
			sealedHolds(t, path, n)
		})
	}
}

// A key that does not open the cache (the keyring's entry replaced) is said, and the
// cache is left exactly as it was: never rebuilt, set aside or written over.
func TestAWrongKeyLeavesTheCacheUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	cache, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, cache, sealedRoom, domain.Message{ID: "$kept", RoomID: sealedRoom, Sender: "@a:example.org", Body: "kept", Timestamp: time.UnixMilli(1)})
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		body, _ := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		before[e.Name()] = body
	}
	replaced := bytes.Repeat([]byte{9}, len(testKey))
	if _, err = Open(ctx, path, replaced); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("Open(another key) error = %v; want ErrWrongKey", err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Errorf("files after a wrong key: %d, before: %d", len(after), len(before))
	}
	for _, e := range after {
		body, _ := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- the test's own folder
		if !bytes.Equal(body, before[e.Name()]) {
			t.Errorf("%s changed under a wrong key", e.Name())
		}
	}
	reopened, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got, _ := reopened.Messages(ctx, sealedRoom, -1); len(got) != 1 || got[0].Body != "kept" {
		t.Errorf("after a wrong key the cache holds %v", got)
	}
}

// The copy a rebuild sets aside is written encrypted, not left in the clear for the
// next open to encrypt: a crash between the two would leave the history readable.
func TestARebuildSetsTheOldCacheAsideEncrypted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	cache := fillCache(t, path, 20)
	if asideErr, err := rebuild(context.Background(), cache.db, path, testKey); err != nil || asideErr != nil {
		t.Fatalf("rebuild() = %v, %v", asideErr, err)
	}
	if left := plainFilesIn(t, dir); len(left) > 0 {
		t.Errorf("the rebuild set the old cache aside in the clear: %v", left)
	}
}

// A snapshot (what a backup carries) is encrypted under the backup's key, not the
// cache's, and is never written in the clear; the copy a rebuild sets aside is
// encrypted too.
func TestCopiesOfTheCacheAreNeverInTheClear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	fillCache(t, path, 30)
	backupKey := bytes.Repeat([]byte{3}, len(testKey))
	snap := filepath.Join(dir, "snapshot.db")
	if err := Snapshot(ctx, path, testKey, snap, backupKey); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(ctx, snap, testKey, true); !errors.Is(err, ErrWrongKey) {
		t.Errorf("the cache's own key opened the snapshot: %v", err)
	}
	if left := plainFilesIn(t, dir); len(left) > 0 {
		t.Errorf("in the clear: %v", left)
	}

	// A plain cache's copies set aside by an earlier rebuild are encrypted on open.
	aside := filepath.Join(t.TempDir(), "cache.db")
	plainCache(t, aside, 10, false)
	if err := os.Rename(aside, path+asideInfix+"20260101-120000"); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AsideFailed(); err != nil {
		t.Errorf("AsideFailed() = %v", err)
	}
	_ = c.Close()
	if left := plainFilesIn(t, dir); len(left) > 0 {
		t.Errorf("in the clear after open: %v", left)
	}
}
