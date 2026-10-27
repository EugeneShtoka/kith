package main

import (
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

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// pair is a fresh backup key: the private half the user keeps, the public half for
// [backup] key.
func pair(t *testing.T) *backup.Key {
	t.Helper()
	id, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// restoreTarget is a [storage] section naming folders of the test's own, so restoring
// a backup of this config touches nothing else; the config written to it is target's
// config.toml.
func restoreTarget(t *testing.T) (section, target string) {
	t.Helper()
	target = t.TempDir()
	return fmt.Sprintf("[storage]\ninstance = \"restored\"\ndata_dir = %q\nstate_dir = %q\ncache_dir = %q\nruntime_dir = %q\n",
		filepath.Join(target, "data"), filepath.Join(target, "state"), filepath.Join(target, "cache"), filepath.Join(target, "run")), target
}

// outsideTmp is an empty folder of the test's own outside /tmp, which [backup] dir
// refuses (kithd's sandbox gives it a /tmp of its own) and where t.TempDir makes its
// folders: one beside the package, named for the process and test, removed when the
// test ends.
func outsideTmp(t *testing.T) string {
	t.Helper()
	dir := fmt.Sprintf(".backups-test-%d-%s", os.Getpid(), strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(abs, "/tmp/") || strings.HasPrefix(abs, "/var/tmp/") {
		t.Skip("the package itself is under /tmp, which [backup] dir refuses")
	}
	return abs
}

// writeConfig replaces d's config with text.
func writeConfig(t *testing.T, d dailyBackups, text string) {
	t.Helper()
	if err := os.WriteFile(d.file.path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// opens reports whether key opens the backup at archive (restored into target).
func opens(t *testing.T, archive string, key *backup.Key, target string) bool {
	t.Helper()
	cacheKey := func(domain.Storage) ([]byte, error) { return bytes.Repeat([]byte{9}, sealeddb.KeyLen), nil }
	_, err := backup.Restore(context.Background(), archive, filepath.Join(target, "config.toml"), true,
		func() (*backup.Key, error) { return key, nil }, cacheKey)
	if err != nil && !errors.Is(err, backup.ErrWrongKey) {
		t.Fatalf("restore %s: %v", filepath.Base(archive), err)
	}
	return err == nil
}

// A [backup] key in the config is the key: `kith backup` uses it without asking or
// keeping one of its own, over a key kept before, and --new-key refuses to replace it.
func TestAKeyInTheConfigIsUsedAndNeverReplaced(t *testing.T) {
	t.Parallel()
	chosen := pair(t)
	cfg := config.Config{Backup: config.Backup{Key: chosen.Recipient().String()}}

	fresh := domain.Storage{Instance: "test", DataDir: t.TempDir()}
	to, err := backupKey(cfg, fresh, false)
	if err != nil || to.String() != chosen.Recipient().String() {
		t.Fatalf("backupKey(config key, none kept) = %v, %v; want the config's key", to, err)
	}
	if kept := keyFileOf(t, fresh); kept != "" {
		t.Errorf("a key was kept beside the config's: %q", kept)
	}

	old := withOldKey(t)
	before := keyFileOf(t, old)
	if to, err := backupKey(cfg, old, false); err != nil || to.String() != chosen.Recipient().String() {
		t.Errorf("backupKey(config key, one kept) = %v, %v; want the config's key over the kept one", to, err)
	}
	if _, err := backupKey(cfg, old, true); !errors.Is(err, errKeyFromConfig) {
		t.Errorf("backupKey(config key, --new-key) = %v; want errKeyFromConfig", err)
	}
	if after := keyFileOf(t, old); after != before {
		t.Errorf("--new-key replaced the kept key though the config names one: %q → %q", before, after)
	}
}

// A [backup] key changed in the config between two scheduled backups switches the
// next one: it opens with the new key only, and the one before with the old only.
func TestScheduledBackupsFollowTheConfigKey(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	section, target := restoreTarget(t)
	oldKey, newKey := pair(t), pair(t)

	writeConfig(t, d, section+fmt.Sprintf("[backup]\nkey = %q\n", oldKey.Recipient()))
	at := time.Now()
	before, err := d.once(context.Background(), at)
	if err != nil || !before.Ran {
		t.Fatalf("the backup under the first key = %+v, %v", before, err)
	}
	writeConfig(t, d, section+fmt.Sprintf("[backup]\nkey = %q\n", newKey.Recipient()))
	after, err := d.once(context.Background(), at.Add(config.DefaultBackupEvery))
	if err != nil || !after.Ran {
		t.Fatalf("the backup under the changed key = %+v, %v", after, err)
	}
	if opens(t, after.Made.Path, oldKey, target) || !opens(t, after.Made.Path, newKey, target) {
		t.Error("the backup after the change does not open with the new key only")
	}
	if opens(t, before.Made.Path, newKey, target) || !opens(t, before.Made.Path, oldKey, target) {
		t.Error("the backup before the change does not open with the old key only")
	}
	if _, statErr := os.Stat(d.storage.BackupKeyPath()); statErr == nil {
		t.Error("a key was kept beside the config's")
	}
}

// With [backup] enabled = false kithd makes no backup, no folder, and says nothing;
// turned on again, the next run backs up.
func TestScheduledBackupsTurnedOffMakeNothing(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	key := pair(t)
	on := fmt.Sprintf("[storage]\ninstance = \"test\"\n[backup]\nkey = %q\n", key.Recipient())
	writeConfig(t, d, on+"enabled = false\n")
	done, err := d.once(context.Background(), time.Now())
	if err != nil || done.Ran {
		t.Fatalf("once(off) = %+v, %v; want nothing done", done, err)
	}
	if _, statErr := os.Stat(d.storage.BackupsDir()); statErr == nil {
		t.Error("a backups folder was made with backups off")
	}
	if tell, _ := d.outcome(done, err); tell != "" {
		t.Errorf("outcome(off) = %q; want nothing said", tell)
	}
	writeConfig(t, d, on+"enabled = true\n")
	if done, err := d.once(context.Background(), time.Now()); err != nil || !done.Ran {
		t.Errorf("once(on again) = %+v, %v; want a backup made", done, err)
	}
}

// A private key pasted into [backup] key while kithd runs makes no backup, and neither
// the error nor what the user is told shows it.
func TestAPrivateKeyInTheConfigMakesNoBackupAndIsNeverShown(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	private := pair(t).String()
	writeConfig(t, d, fmt.Sprintf("[storage]\ninstance = \"test\"\n[backup]\nkey = %q\n", private))
	done, err := d.once(context.Background(), time.Now())
	if err == nil || done.Ran {
		t.Fatalf("once(private key) = %+v, %v; want it refused", done, err)
	}
	tell, _ := d.outcome(done, err)
	secret := strings.TrimPrefix(private, "AGE-SECRET-KEY-")
	for _, text := range []string{err.Error(), tell} {
		if strings.Contains(strings.ToUpper(text), strings.ToUpper(secret)) {
			t.Errorf("the private key is shown: %q", text)
		}
	}
	if _, statErr := os.Stat(d.storage.BackupsDir()); statErr == nil {
		t.Error("a backups folder was made under a private key")
	}
}

// A config changed while a backup runs (here, waiting for the vacuum) is the next run's:
// the running one finishes as it started, into its folder; the next, with backups
// now off, makes nothing and no new folder.
func TestAConfigChangedDuringABackupIsTheNextRuns(t *testing.T) {
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	var maintenance sync.Mutex
	d := dailyFor(t, &said, &maintenance)
	key := pair(t)
	own := outsideTmp(t)
	first, second := filepath.Join(own, "first"), filepath.Join(own, "second")
	writeConfig(t, d, fmt.Sprintf("[storage]\ninstance = \"test\"\n[backup]\nkey = %q\ndir = %q\n", key.Recipient(), first))

	maintenance.Lock() // the vacuum is running
	finished := make(chan backup.Daily, 1)
	go func() {
		done, err := d.once(context.Background(), time.Now())
		if err != nil {
			t.Errorf("the running backup = %v", err)
		}
		finished <- done
	}()
	time.Sleep(200 * time.Millisecond) // it has read its config and waits for the vacuum
	writeConfig(t, d, fmt.Sprintf("[storage]\ninstance = \"test\"\n[backup]\nenabled = false\nkey = %q\ndir = %q\n", key.Recipient(), second))
	maintenance.Unlock()
	if done := <-finished; !done.Ran || filepath.Dir(done.Made.Path) != first {
		t.Fatalf("the running backup = %+v; want it made into %s", done, first)
	}
	if done, err := d.once(context.Background(), time.Now().Add(config.DefaultBackupEvery)); err != nil || done.Ran {
		t.Errorf("the next run = %+v, %v; want nothing: backups are off now", done, err)
	}
	if _, statErr := os.Stat(second); statErr == nil {
		t.Error("the new folder was made though backups are off")
	}
}

// A backups folder kithd may not write fails the backup, which is told, and leaves
// nothing in it.
func TestAFolderKithdMayNotWriteFailsTheBackup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	keyringTurn.Lock()
	defer keyringTurn.Unlock()
	defer keyring.MockInit()
	keyring.MockInit()
	var said []string
	d := dailyFor(t, &said, &sync.Mutex{})
	shut := filepath.Join(outsideTmp(t), "shut")
	if err := os.Mkdir(shut, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o700) })
	writeConfig(t, d, fmt.Sprintf("[storage]\ninstance = \"test\"\n[backup]\nkey = %q\ndir = %q\n",
		pair(t).Recipient(), filepath.Join(shut, "backups")))
	done, err := d.once(context.Background(), time.Now())
	if err == nil || done.Ran {
		t.Fatalf("once(unwritable folder) = %+v, %v; want it failed", done, err)
	}
	if tell, _ := d.outcome(done, err); tell == "" {
		t.Error("the failure was not told")
	}
	if entries, _ := os.ReadDir(shut); len(entries) != 0 {
		t.Errorf("the folder holds %d files; want none", len(entries))
	}
}

// kithd looks again when the next backup is due, a second late, but never later than
// dailyEvery, and in dailyEvery when no time is known (backups off, no key, a failure).
func TestTheNextLookIsWhenTheNextBackupIsDue(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for next, want := range map[time.Time]time.Duration{
		{}:                              dailyEvery,
		now.Add(10 * time.Minute):       10*time.Minute + dailyLate,
		now.Add(5 * time.Hour):          dailyEvery,
		now.Add(-time.Minute):           dailyLate,
		now.Add(dailyEvery - dailyLate): dailyEvery,
	} {
		if got := nextLook(backup.Daily{Next: next}, now); got != want {
			t.Errorf("nextLook(next in %v) = %v; want %v", next.Sub(now), got, want)
		}
	}
}
