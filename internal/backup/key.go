package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every backup, made by hand or by kithd's schedule, is encrypted to one age key. The
// machine keeps only its public half ([backup] key, else Storage.BackupKeyPath), which
// can lock a backup but never open one; the private half is the user's: shown once
// when the first manual backup makes it, or made by the user for [backup] key, and
// kith keeps no copy. So nothing on the machine can read a backup, and losing the
// private key loses every backup made to it.

// ErrNoKey is a machine with no backup key yet: the first manual backup makes one.
var ErrNoKey = errors.New("no backup key yet")

// Key is a backup key's private half, the user's; PublicKey its public half, the
// machine's. age's own types, named here so its callers need no age.
type (
	Key       = age.X25519Identity
	PublicKey = age.X25519Recipient
)

// NewKey makes a backup key. Its private half must reach the user before its public
// half is kept (SaveKey): a public key whose private half nobody saw locks backups no
// one can open.
func NewKey() (*Key, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("backup: make a key: %w", err)
	}
	return id, nil
}

// LoadKey is the public key storage's backups are encrypted to; ErrNoKey when there is
// none yet.
func LoadKey(storage domain.Storage) (*PublicKey, error) {
	body, err := os.ReadFile(storage.BackupKeyPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("backup: the backup key: %w", err)
	}
	to, err := age.ParseX25519Recipient(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, fmt.Errorf("backup: the backup key at %s: %w", storage.BackupKeyPath(), err)
	}
	return to, nil
}

// KeyFor is the public key cfg's backups are encrypted to: [backup] key when the
// config sets one (fromConfig), else the one the first backup kept (LoadKey).
func KeyFor(cfg config.Config, storage domain.Storage) (to *PublicKey, fromConfig bool, err error) {
	set := strings.TrimSpace(cfg.Backup.Key)
	if set == "" {
		to, err = LoadKey(storage)
		return to, false, err
	}
	to, err = age.ParseX25519Recipient(set)
	if err != nil {
		// setup.Backups refuses this at startup; never echo it, it may be a private key.
		return nil, true, errors.New("backup: [backup] key is not an age public key (it starts age1…)")
	}
	return to, true, nil
}

// SaveKey keeps to as storage's backup key, written whole or not at all.
func SaveKey(storage domain.Storage, to *PublicKey) error {
	dest := storage.BackupKeyPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+"-*")
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	_, err = f.WriteString(to.String() + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), dest)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("backup: keep the backup key: %w", err)
	}
	return nil
}

// ParseKey reads a private backup key as the user saved it: the AGE-SECRET-KEY line,
// alone or in an age identity file among comments.
func ParseKey(text string) (*Key, error) {
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, fmt.Errorf("%w: not a backup key (it starts AGE-SECRET-KEY-1)", ErrWrongKey)
		}
		return id, nil
	}
	return nil, fmt.Errorf("%w: no key in it", ErrWrongKey)
}
