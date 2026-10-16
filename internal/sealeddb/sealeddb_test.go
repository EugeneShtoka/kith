package sealeddb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, sealeddb.KeyLen) }

// What is written comes back with the same key, and nothing of it is on disk in the
// clear: not the database's header, not a value written, in the file or its log.
func TestASealedStoreKeepsItsContentsUnreadable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sealeddb.Open(ctx, path, key(1))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "identity-key-of-15550100001"
	if _, err = db.ExecContext(ctx, "CREATE TABLE keys (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO keys VALUES (?)", secret); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal"} {
		raw, rerr := os.ReadFile(f)
		if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			t.Fatal(rerr)
		}
		if bytes.Contains(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%s holds the store in the clear", filepath.Base(f))
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sealeddb.Open(ctx, path, key(1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var got string
	if err := db.QueryRowContext(ctx, "SELECT v FROM keys").Scan(&got); err != nil || got != secret {
		t.Errorf("read back %q (%v)", got, err)
	}
}

// Another key's store, or a plain database, does not open: the error says the key does
// not open it, at once, so it is never taken for an empty store.
func TestAWrongKeyIsSaidAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	sealed := filepath.Join(dir, "sealed.db")
	db, err := sealeddb.Open(ctx, sealed, key(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "CREATE TABLE t (v)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := sealeddb.Open(ctx, sealed, key(2)); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("another key: %v, want ErrWrongKey", err)
	}
	plain := filepath.Join(dir, "plain.db")
	if err := os.WriteFile(plain, append([]byte("SQLite format 3\x00"), make([]byte, 4096-16)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sealeddb.Open(ctx, plain, key(1)); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("a plain database: %v, want ErrWrongKey", err)
	}
	if _, err := sealeddb.Open(ctx, filepath.Join(dir, "x.db"), []byte("short")); err == nil {
		t.Error("a key of the wrong length was taken")
	}
}

// plainStore writes a plain database at path as the stores were before: two tables,
// the second row still in the write-ahead log when the database is left.
func plainStore(t *testing.T, path string) {
	t.Helper()
	src := path + ".src"
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE devices (jid TEXT PRIMARY KEY)", "CREATE TABLE sessions (addr TEXT, v BLOB)",
		"INSERT INTO devices VALUES ('15550100001.0:1@s.whatsapp.net')",
		"INSERT INTO sessions VALUES ('15550100009.0', x'01'), ('15550100010.0', x'02')",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	// Copied while open, as a daemon that stopped dead leaves it: the rows are in the
	// log, not yet in the database file.
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(src + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	if raw, _ := os.ReadFile(path + "-wal"); !bytes.Contains(raw, []byte("15550100009")) {
		t.Fatal("the fixture's rows did not stay in the log")
	}
}

// readBack checks the store at path opens with key and holds plainStore's rows, and
// that nothing of the plain store is left beside it.
func readBack(t *testing.T, path string, k []byte) {
	t.Helper()
	db, err := sealeddb.Open(context.Background(), path, k)
	if err != nil {
		t.Fatalf("the migrated store does not open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var devices, sessions int
	if err := db.QueryRowContext(context.Background(), "SELECT (SELECT count(*) FROM devices), (SELECT count(*) FROM sessions)").Scan(&devices, &sessions); err != nil || devices != 1 || sessions != 2 {
		t.Errorf("read back %d devices, %d sessions (%v), want 1 and 2", devices, sessions, err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.HasPrefix(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte("15550100009")) {
		t.Error("the store is still in the clear")
	}
	for _, left := range []string{".plain", ".sealing", ".plain-wal"} {
		if _, err := os.Stat(path + left); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left beside the store", left)
		}
	}
}

// A plain store is turned into an encrypted one, whole, the rows its log held too; a
// fresh store is encrypted from the start; and the migrated store refuses another key.
func TestAPlainStoreIsEncryptedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "whatsapp.db")
	plainStore(t, path)
	db, err := sealeddb.OpenMigrating(ctx, path, key(1))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	readBack(t, path, key(1))
	if _, err = sealeddb.OpenMigrating(ctx, path, key(2)); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("another key on the migrated store: %v, want ErrWrongKey", err)
	}
	fresh := filepath.Join(dir, "messenger.db")
	db, err = sealeddb.OpenMigrating(ctx, fresh, key(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (v)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if raw, _ := os.ReadFile(fresh); bytes.HasPrefix(raw, []byte("SQLite format 3")) {
		t.Error("a fresh store was made in the clear")
	}
}

// A migration interrupted anywhere is taken up again at the next open, and ends with
// every row and nothing in the clear: a half copy beside the plain store, the plain
// store set aside before the copy took its name, or set aside after it did.
func TestAnInterruptedMigrationIsTakenUpAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		crash func(t *testing.T, path string)
	}{
		{"a half-written copy", func(t *testing.T, path string) {
			if err := os.WriteFile(path+".sealing", []byte("half a copy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"the plain store set aside, the copy not in place", func(t *testing.T, path string) {
			if err := os.Rename(path, path+".plain"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".sealing", []byte("an unchecked copy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"the copy in place, the plain store not yet deleted", func(t *testing.T, path string) {
			if err := os.Rename(path, path+".plain"); err != nil {
				t.Fatal(err)
			}
			plainStore(t, path+".copy")
			db, err := sealeddb.OpenMigrating(ctx, path+".copy", key(1))
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if err := os.Rename(path+".copy", path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "whatsapp.db")
			plainStore(t, path)
			tc.crash(t, path)
			db, err := sealeddb.OpenMigrating(ctx, path, key(1))
			if err != nil {
				t.Fatalf("after %s: %v", tc.name, err)
			}
			_ = db.Close()
			readBack(t, path, key(1))
		})
	}
}
