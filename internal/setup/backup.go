package setup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/human"
)

// errPrivateBackupKey is [backup] key holding the private key. Its words never include
// the value: whatever prints or logs a config error must not spread the one thing that
// opens every backup.
var errPrivateBackupKey = errors.New("backup.key: this is the private key; put only the public key " +
	"(age1…) here and keep the private key off this machine (`age-keygen -y` prints its public half)")

// Backups checks [backup]: a public key (never the private one), an interval of at
// least config.MinBackupEvery, at least one backup kept, and a folder kithd can be
// given (a full path, and not under /tmp, which kithd's sandbox replaces).
func Backups(b config.Backup) error {
	if key := strings.TrimSpace(b.Key); key != "" {
		if strings.Contains(strings.ToUpper(key), "AGE-SECRET-KEY-") {
			return errPrivateBackupKey
		}
		if _, err := age.ParseX25519Recipient(key); err != nil {
			return errors.New("backup.key: not an age public key (it starts age1…)")
		}
	}
	if strings.TrimSpace(b.Every) != "" {
		every, err := config.ParseEvery(b.Every)
		if err != nil {
			return fmt.Errorf("backup.every %q: write a duration like 6h or 90m, or days like 7d", b.Every)
		}
		if every < config.MinBackupEvery {
			return fmt.Errorf("backup.every %q: at least %s (each backup copies the whole cache)", b.Every, human.Duration(config.MinBackupEvery))
		}
	}
	if b.KeepOrDefault() < 1 {
		return fmt.Errorf("backup.keep %d: keep at least 1", b.KeepOrDefault())
	}
	return backupDir(b.Dir)
}

// backupDir checks [backup] dir: empty, or a full path (or one starting with ~/)
// outside /tmp and /var/tmp.
func backupDir(dir string) error {
	if dir == "" {
		return nil
	}
	if dir != "~" && !strings.HasPrefix(dir, "~/") && !filepath.IsAbs(dir) {
		return fmt.Errorf("backup.dir %q: give a full path (or one starting with ~/)", dir)
	}
	clean := filepath.Clean(dirOr(dir, ""))
	for _, tmp := range []string{"/tmp", "/var/tmp"} {
		if clean == tmp || strings.HasPrefix(clean, tmp+"/") {
			return fmt.Errorf("backup.dir %q: kithd's sandbox gives it a /tmp of its own, so backups there would vanish; choose a folder elsewhere", dir)
		}
	}
	return nil
}
