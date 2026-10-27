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

// A private key in [backup] key is refused however it was pasted, and the refusal
// never carries it, at startup too; a public key is taken.
func TestAPrivateBackupKeyIsRefusedAndNeverEchoed(t *testing.T) {
	t.Parallel()
	private, public := backupKeys(t)
	if err := setup.Backups(config.Backup{Key: public}); err != nil {
		t.Fatalf("a public key: %v; want it taken", err)
	}
	secret := strings.ToUpper(strings.TrimPrefix(private, "AGE-SECRET-KEY-"))
	for _, pasted := range []string{private, "  " + private + "\n", strings.ToLower(private), "# my key\n" + private} {
		for _, err := range []error{
			setup.Backups(config.Backup{Key: pasted}),
			setup.Validate(config.Config{Backup: config.Backup{Key: pasted}}),
		} {
			if err == nil {
				t.Errorf("a private key, pasted as %q…, was taken", pasted[:12])
			} else if strings.Contains(strings.ToUpper(err.Error()), secret) {
				t.Errorf("the refusal carries the private key: %v", err)
			}
		}
	}
}

// every is a readable interval, keep at least 1, and dir a full path outside the /tmp
// kithd's sandbox replaces.
func TestTheBackupScheduleAndFolderAreChecked(t *testing.T) {
	t.Parallel()
	zero, one := 0, 1
	for _, ok := range []config.Backup{
		{}, {Every: "1m"}, {Every: "7d"}, {Keep: &one}, {Dir: "~/backups"}, {Dir: "/mnt/nas/kith"},
	} {
		if err := setup.Backups(ok); err != nil {
			t.Errorf("Backups(%+v) = %v; want it taken", ok, err)
		}
	}
	for _, refused := range []config.Backup{
		{Every: "0d"}, {Keep: &zero},
		{Dir: "backups"}, {Dir: "/tmp/kith"}, {Dir: "/var/tmp"}, {Dir: "/tmp/../tmp/x"},
	} {
		if err := setup.Backups(refused); err == nil {
			t.Errorf("Backups(%+v) was taken; want it refused", refused)
		}
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
