package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/db"
)

// dailyFor is kithd's daily backup of the instance lockedInstance made (its cache under
// key, in the in-memory keyring), its config written beside it, telling what it says
// into said.
func dailyFor(t *testing.T, said *[]string, maintenance *sync.Mutex) dailyBackups {
	t.Helper()
	storage, lock, key := lockedInstance(t)
	_ = lock.Release()
	storage.ExportDir = filepath.Join(storage.DataDir, "exports")
	if err := keyring.Set(storage.KeyringService, storage.Instance+"|cache-store", base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(storage.DataDir, "config.toml")
	if err := os.WriteFile(path, []byte("[storage]\ninstance = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dailyBackups{
		log: slog.New(slog.DiscardHandler), file: configFile{path: path}, storage: storage,
		say: func(body string) { *said = append(*said, body) }, maintenance: maintenance,
	}
}

// Without a backup key kithd makes no backup and no key (nobody would see its private
// half), and says backups are off until `kith backup` runs once.
func TestNoBackupKeyMeansNoDailyBackupAndNoKeyMade(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	done, err := d.once(context.Background(), time.Now())
	if !errors.Is(err, backup.ErrNoKey) || done.Ran {
		t.Fatalf("once() without a key = %+v, %v; want ErrNoKey", done, err)
	}
	if _, statErr := os.Stat(d.storage.BackupKeyPath()); statErr == nil {
		t.Fatal("kithd made a backup key itself")
	}
	if _, statErr := os.Stat(d.storage.BackupsDir()); statErr == nil {
		t.Fatal("kithd made a backups folder with no key to back up to")
	}
	if tell, _ := d.outcome(done, err); tell != noKeySaid {
		t.Errorf("outcome() = %q; want %q", tell, noKeySaid)
	}
}

// With a key, the daily backup is made into the backups folder, and only while the
// vacuum is not running (they share the maintenance lock).
func TestADailyBackupWaitsForTheVacuum(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	var maintenance sync.Mutex
	d := dailyFor(t, &said, &maintenance)
	id, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.SaveKey(d.storage, id.Recipient()); err != nil {
		t.Fatal(err)
	}

	maintenance.Lock() // the vacuum is running
	type result struct {
		done backup.Daily
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		done, err := d.once(context.Background(), time.Now())
		finished <- result{done, err}
	}()
	select {
	case r := <-finished:
		t.Fatalf("the daily backup ran while the vacuum held the cache: %+v, %v", r.done, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	maintenance.Unlock()
	r := <-finished
	if r.err != nil || !r.done.Ran {
		t.Fatalf("once() = %+v, %v; want a backup made", r.done, r.err)
	}
	if filepath.Dir(r.done.Made.Path) != d.storage.BackupsDir() {
		t.Errorf("made %s; want it in %s", r.done.Made.Path, d.storage.BackupsDir())
	}
	if tell, _ := d.outcome(r.done, r.err); tell != "" {
		t.Errorf("outcome() of a backup made = %q; want nothing said", tell)
	}
}

// A failure is said; a disk without room is put off, not called a failure.
func TestADailyBackupsTroubleIsSaid(t *testing.T) {
	t.Parallel()
	d := dailyBackups{log: slog.New(slog.DiscardHandler)}
	for err, want := range map[error]string{
		errors.New("the keyring is not answering"): "The scheduled backup failed: the keyring is not answering",
		errors.Join(db.ErrNoRoom, errors.New("x")): "The scheduled backup is put off: not enough free disk space\nx",
		errDailyPanicked: "The scheduled backup failed: the daily backup panicked",
	} {
		if got, _ := d.outcome(backup.Daily{}, err); got != want {
			t.Errorf("outcome(%v) = %q; want %q", err, got, want)
		}
	}
}
