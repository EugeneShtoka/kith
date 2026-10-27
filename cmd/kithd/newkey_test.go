package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// keyFileOf is the public backup key kept at storage, as text ("" when there is none).
func keyFileOf(t *testing.T, storage domain.Storage) string {
	t.Helper()
	body, err := os.ReadFile(storage.BackupKeyPath())
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// withOldKey is a storage whose backup key is already kept.
func withOldKey(t *testing.T) domain.Storage {
	t.Helper()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, ExportDir: filepath.Join(dir, "exports")}
	old, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.SaveKey(storage, old.Recipient()); err != nil {
		t.Fatal(err)
	}
	return storage
}

// A new key replaces the one kept only when the user says yes; any other answer, or
// none, keeps the old key and says nothing changed.
func TestANewKeyIsConfirmedBeforeTheOldIsReplaced(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"\n", "n\n", "no\n", "later\n"} {
		storage := withOldKey(t)
		before := keyFileOf(t, storage)
		if _, err := makeKey(storage, true, bufio.NewReader(strings.NewReader(answer))); !errors.Is(err, errKeyKept) {
			t.Errorf("makeKey(answer %q) = %v; want errKeyKept", answer, err)
		}
		if after := keyFileOf(t, storage); after != before {
			t.Errorf("answer %q replaced the key: %q → %q", answer, before, after)
		}
	}
	for _, answer := range []string{"y\n\n", "YES\n\n"} {
		storage := withOldKey(t)
		before := keyFileOf(t, storage)
		to, err := makeKey(storage, true, bufio.NewReader(strings.NewReader(answer)))
		if err != nil {
			t.Fatalf("makeKey(answer %q) = %v; want a new key", answer, err)
		}
		after := keyFileOf(t, storage)
		if after == before || strings.TrimSpace(after) != to.String() {
			t.Errorf("answer %q: kept %q (was %q); want the new key %s", answer, after, before, to)
		}
	}
}

// A run cut short before the new private key is kept (Ctrl-C, a closed terminal)
// leaves the old key in place: a public key whose private half nobody kept would lock
// backups no one can open.
func TestANewKeyCutShortLeavesTheOldKey(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"before the confirmation": "",
		"at the keep prompt":      "y\n",
		"while saving to a file":  "y\n" + filepath.Join(t.TempDir(), "no", "such", "dir", "key.txt") + "\n",
	} {
		storage := withOldKey(t)
		before := keyFileOf(t, storage)
		if _, err := makeKey(storage, true, bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Errorf("%s: makeKey() = nil; want the cut-short run to fail", name)
		}
		if after := keyFileOf(t, storage); after != before {
			t.Errorf("%s: the old key was replaced: %q → %q", name, before, after)
		}
	}
}

// Without a terminal, --new-key refuses: the new key could not be shown to anyone.
func TestNewKeyWithoutATerminalRefuses(t *testing.T) {
	t.Parallel()
	if term.IsTerminal(int(os.Stdin.Fd())) { // #nosec G115 -- a file descriptor fits an int
		t.Skip("stdin is a terminal here")
	}
	storage := withOldKey(t)
	before := keyFileOf(t, storage)
	_, err := backupKey(config.Config{}, storage, true)
	if err == nil {
		t.Error("backupKey(new key, no terminal) made a key nobody could see")
	}
	if after := keyFileOf(t, storage); after != before {
		t.Errorf("the key was replaced without a terminal: %q → %q", before, after)
	}
}

// After a new key, kithd's next daily backup is encrypted to it: the new key opens
// it and the old one does not, while the dailies made before still open with the old
// key only.
func TestDailyBackupsSwitchToANewKey(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	// The backed-up config names a restore target of its own, so restoring touches
	// only this test's folders.
	target := t.TempDir()
	config := fmt.Sprintf("[storage]\ninstance = \"restored\"\ndata_dir = %q\nstate_dir = %q\ncache_dir = %q\nruntime_dir = %q\n",
		filepath.Join(target, "data"), filepath.Join(target, "state"), filepath.Join(target, "cache"), filepath.Join(target, "run"))
	if err := os.WriteFile(d.file.path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	keys := t.TempDir()
	oldFile, newFile := filepath.Join(keys, "old.txt"), filepath.Join(keys, "new.txt")
	if _, err := makeKey(d.storage, false, bufio.NewReader(strings.NewReader(oldFile+"\n"))); err != nil {
		t.Fatal(err)
	}
	day := time.Now()
	before, err := d.once(context.Background(), day)
	if err != nil || !before.Ran {
		t.Fatalf("the daily backup before the new key = %+v, %v", before, err)
	}
	if _, kerr := makeKey(d.storage, true, bufio.NewReader(strings.NewReader("y\n"+newFile+"\n"))); kerr != nil {
		t.Fatal(kerr)
	}
	after, err := d.once(context.Background(), day.Add(24*time.Hour))
	if err != nil || !after.Ran {
		t.Fatalf("the daily backup after the new key = %+v, %v", after, err)
	}

	oldKey, err := readKey(oldFile)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := readKey(newFile)
	if err != nil {
		t.Fatal(err)
	}
	cacheKey := func(domain.Storage) ([]byte, error) { return bytes.Repeat([]byte{9}, sealeddb.KeyLen), nil }
	restore := func(archive string, key *backup.Key, replace bool) error {
		_, err := backup.Restore(context.Background(), archive, filepath.Join(target, "config.toml"), replace,
			func() (*backup.Key, error) { return key, nil }, cacheKey)
		return err
	}
	if err := restore(after.Made.Path, oldKey, false); !errors.Is(err, backup.ErrWrongKey) {
		t.Errorf("the old key on the daily made after the new key = %v; want ErrWrongKey", err)
	}
	if err := restore(after.Made.Path, newKey, false); err != nil {
		t.Errorf("the new key on the daily made after it = %v; want it restored", err)
	}
	if err := restore(before.Made.Path, newKey, true); !errors.Is(err, backup.ErrWrongKey) {
		t.Errorf("the new key on the daily made before it = %v; want ErrWrongKey", err)
	}
	if err := restore(before.Made.Path, oldKey, true); err != nil {
		t.Errorf("the old key on the daily made before the new one = %v; want it restored", err)
	}
}
