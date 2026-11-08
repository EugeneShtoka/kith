package sealeddb_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

// Every file of a store, the log and shared memory too, is the owner's alone, whatever
// the umask.
func TestAStoreIsTheOwnersAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "messenger.db")
	db, err := sealeddb.Open(ctx, path, key(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (v)"); err != nil {
		t.Fatal(err)
	}
	ownerOnly(t, path)
	_ = db.Close()
}

// ownerOnly checks the store at path, its log and its shared memory, those there, are
// the owner's alone.
func ownerOnly(t *testing.T, path string) {
	t.Helper()
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(f)
		if errors.Is(err, os.ErrNotExist) && f != path {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s is %v; want 0600, the owner's alone", filepath.Base(f), perm)
		}
	}
}
