package setup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// backupKeys is a fresh key pair as text: its private and its public half.
func backupKeys(t *testing.T) (private, public string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.String(), id.Recipient().String()
}

// A private key in [backup] key is refused, loudly and without its value, however it
// was pasted; so is anything that is not a public key. The startup check refuses it too.
func TestTheBackupKeyIsAPublicKeyAndNeverEchoed(t *testing.T) {
	t.Parallel()
	private, public := backupKeys(t)
	if err := setup.Backups(config.Backup{Key: public}); err != nil {
		t.Fatalf("a public key: %v; want it taken", err)
	}
	secret := strings.TrimPrefix(private, "AGE-SECRET-KEY-")
	for _, pasted := range []string{private, "  " + private + "\n", strings.ToLower(private), "# my key\n" + private} {
		err := setup.Backups(config.Backup{Key: pasted})
		if err == nil || !strings.Contains(err.Error(), "this is the private key") {
			t.Errorf("a private key, pasted as %q…: %v; want it refused as the private key", pasted[:12], err)
			continue
		}
		if strings.Contains(strings.ToUpper(err.Error()), strings.ToUpper(secret)) {
			t.Errorf("the refusal shows the private key: %v", err)
		}
		if verr := setup.Validate(config.Config{Backup: config.Backup{Key: pasted}}); verr == nil ||
			strings.Contains(strings.ToUpper(verr.Error()), strings.ToUpper(secret)) {
			t.Errorf("Validate with a private key = %v; want a refusal without the key", verr)
		}
	}
	for _, wrong := range []string{"age1qq", "age1notakeyatall", public[:len(public)-3], "ssh-ed25519 AAAA"} {
		err := setup.Backups(config.Backup{Key: wrong})
		if err == nil || !strings.Contains(err.Error(), "backup.key") || strings.Contains(err.Error(), wrong) {
			t.Errorf("Backups(key %q) = %v; want backup.key refused, the value not repeated", wrong, err)
		}
	}
}

// every is at least an hour and readable, keep at least 1, and dir a full path outside
// the /tmp kithd's sandbox replaces.
func TestTheBackupScheduleAndFolderAreChecked(t *testing.T) {
	t.Parallel()
	zero, one := 0, 1
	for _, ok := range []config.Backup{
		{}, {Every: "1h"}, {Every: "7d"}, {Keep: &one}, {Dir: "~/backups"}, {Dir: "/mnt/nas/kith"},
	} {
		if err := setup.Backups(ok); err != nil {
			t.Errorf("Backups(%+v) = %v; want it taken", ok, err)
		}
	}
	for b, want := range map[*config.Backup]string{
		{Every: "59m"}:            "backup.every",
		{Every: "weekly"}:         "backup.every",
		{Keep: &zero}:             "backup.keep",
		{Dir: "backups"}:          "backup.dir",
		{Dir: "/tmp/kith"}:        "backup.dir",
		{Dir: "/var/tmp"}:         "backup.dir",
		{Dir: "/tmp/../tmp/x"}:    "backup.dir",
		{Every: "0d", Keep: &one}: "backup.every",
	} {
		if err := setup.Backups(*b); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Backups(%+v) = %v; want %s refused", *b, err, want)
		}
	}
	// The minimum is said as people write it, from the setting itself.
	if err := setup.Backups(config.Backup{Every: "30m"}); err == nil || !strings.Contains(err.Error(), "at least 1h ") {
		t.Errorf("Backups(every 30m) = %v; want it to say at least 1h", err)
	}
}

// Scheduled backups go to [backup] dir, "~" its home, else <export_dir>/backups.
func TestTheBackupsFolder(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		"/mnt/nas/kith":  "/mnt/nas/kith",
		"~/kith-backups": filepath.Join(home, "kith-backups"),
	} {
		storage, serr := setup.StorageDirs(config.Config{Backup: config.Backup{Dir: dir}})
		if serr != nil || storage.BackupsDir() != want {
			t.Errorf("[backup] dir %q: %q, %v; want %q", dir, storage.BackupsDir(), serr, want)
		}
	}
	storage, err := setup.StorageDirs(config.Config{Storage: config.Storage{ExportDir: "/srv/exports"}})
	if err != nil || storage.BackupsDir() != "/srv/exports/backups" {
		t.Errorf("no [backup] dir: %q, %v; want <export_dir>/backups", storage.BackupsDir(), err)
	}
	if _, err := setup.StorageDirs(config.Config{Backup: config.Backup{Dir: "relative"}}); err == nil {
		t.Error("a relative [backup] dir was taken")
	}
}
