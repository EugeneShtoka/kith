package backup_test

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// fullDiskEnv carries what a child runs where no file may grow past a limit: the
// limit, the operation, then its paths, one per line.
const fullDiskEnv = "KITH_TEST_FULL_DISK"

// onAFullDisk runs this test again in a child whose files cannot grow past limit
// bytes (RLIMIT_FSIZE), so a real write fails as on a full disk, and returns what it
// printed. The child must fail.
func onAFullDisk(t *testing.T, limit int64, op string, paths ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^"+t.Name()+"$") // #nosec G204 -- this test binary
	cmd.Env = append(os.Environ(), fullDiskEnv+"="+strings.Join(append([]string{strconv.FormatInt(limit, 10), op}, paths...), "\n"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the %s on a full disk: %v\n%s", op, err, out)
	}
	if !strings.Contains(string(out), "file too large") {
		t.Fatalf("the %s did not fail writing: %s", op, out)
	}
	return string(out)
}

// childOnAFullDisk is the child's side: it caps its files, runs the operation and
// exits 0 only if it failed.
func childOnAFullDisk() bool {
	args := os.Getenv(fullDiskEnv)
	if args == "" {
		return false
	}
	lines := strings.Split(args, "\n")
	limit, _ := strconv.ParseUint(lines[0], 10, 64)
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
		println("setrlimit:", err.Error())
		os.Exit(2)
	}
	var err error
	switch lines[1] {
	case "restore":
		_, err = backup.Restore(context.Background(), lines[2], lines[3], true, func() (string, error) { return passphrase, nil }, keyOf)
	case "backup":
		cfg, lerr := config.Load(lines[2])
		storage, serr := setup.StorageFor(cfg, lines[2])
		if lerr != nil || serr != nil {
			println("config:", lerr, serr)
			os.Exit(2)
		}
		_, err = backup.Create(context.Background(), backup.Source{ConfigPath: lines[2], Config: cfg, Storage: storage, CacheKey: keyAt(storage.DataDir)}, lines[3], passphrase)
	}
	if err == nil {
		println("the", lines[1], "succeeded")
		os.Exit(3)
	}
	println(err.Error())
	os.Exit(0)
	return true
}

// A disk that fills while the cache is being written fails the restore, and the files
// there (the cache it would replace among them) stay as they were.
func TestARestoreOnAFullDiskLeavesEverythingAsItWas(t *testing.T) {
	if childOnAFullDisk() {
		return
	}
	old := newMachine(t)
	old.as(t).fill(t, "kept", 3000)
	archive := old.backUp(t)
	there := newMachine(t).as(t)
	there.fill(t, "local", 10)
	before := fingerprint(t, there.home)
	onAFullDisk(t, 64<<10, "restore", archive, there.configPath)
	unchanged(t, there.home, before)
}

// A disk that fills while the backup itself is written (the cache's copy fits, the
// backup holding it does not) leaves no file where it was going, and no copy behind.
func TestABackupOnAFullDiskLeavesNoFile(t *testing.T) {
	if childOnAFullDisk() {
		return
	}
	m := newMachine(t)
	m.as(t).fill(t, "kept", 3000)
	probe := filepath.Join(t.TempDir(), "probe.db")
	key := keyAt(m.storage(t).DataDir)
	if err := db.Snapshot(context.Background(), m.storage(t).CachePath(), key, probe, key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, m.home)
	outDir := t.TempDir()
	onAFullDisk(t, info.Size()+1024, "backup", m.configPath, filepath.Join(outDir, "kith-backup.tar.age"))
	if left, _ := os.ReadDir(outDir); len(left) != 0 {
		t.Fatalf("a backup that ran out of disk left %v", left)
	}
	unchanged(t, m.home, before)
}
