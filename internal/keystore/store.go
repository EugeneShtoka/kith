package keystore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const ownerOnly fs.FileMode = 0o600

// Store locates one instance's session and encryption key: keyring service, account
// and fallback file.
type Store struct {
	Service string
	Account string
	File    string
}

// ErrNoSecretStore means no OS secret store and no file fallback; a warning, not fatal.
var ErrNoSecretStore = errors.New(
	"session: no OS secret store available; token not persisted (set allow_token_file to persist to a 0600 file)")

// ReadOwnerOnly reads a fallback file holding what, refusing one others can read;
// found is false when there is none.
func ReadOwnerOnly(path, what string) (blob []byte, found bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("keystore: stat %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&^ownerOnly != 0 {
		return nil, false, fmt.Errorf(
			"keystore: %s is group/other-accessible (%#o); it holds %s — run: chmod 600 %s",
			path, perm, what, path)
	}
	blob, err = os.ReadFile(path) // #nosec G304 -- a fallback file, mode-checked above
	if err != nil {
		return nil, false, fmt.Errorf("keystore: read %s: %w", path, err)
	}
	return blob, true, nil
}

// SaveFile writes blob to path, mode 0600, atomically, so a failed save never destroys
// what was there.
func SaveFile(path string, blob []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("keystore: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".keystore-*.tmp")
	if err != nil {
		return fmt.Errorf("keystore: mktemp beside %s: %w", path, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(blob)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, ownerOnly)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name) // cleanup on the error path; err is the report
		return fmt.Errorf("keystore: write %s: %w", path, err)
	}
	return nil
}

// RemoveFile removes the fallback file at path, if there is one.
func RemoveFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("keystore: remove %s: %w", path, err)
	}
	return nil
}
