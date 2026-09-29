// Package session persists login credentials in the OS secret store (via go-keyring,
// no cgo). Without a secret store the token is not persisted unless the caller opts
// into a per-account 0600 file in the XDG state dir (allowFile), which is removed as
// soon as the keyring becomes usable again.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	service                  = "kith"
	pickleKeyLen             = 32
	ownerOnly    fs.FileMode = 0o600
)

// ErrNoSecretStore is returned when no OS secret store is usable and the file
// fallback was not opted into. It is a warning to relay, not a fatal error.
var ErrNoSecretStore = errors.New(
	"session: no OS secret store available; token not persisted (set allow_token_file to persist to a 0600 file)")

var errEmptySecret = errors.New("session: a secret needs a name and a value")

// stateDir is the file-fallback directory.
func stateDir() string { return filepath.Join(xdg.StateHome, "kith") }

func sessionPath(account string) string {
	return filepath.Join(stateDir(), fmt.Sprintf("session-%s.toml", domain.AccountKey(account)))
}

// Save stores the session for account in the OS secret store, or in the 0600 file
// when the store is unavailable and allowFile is set.
func Save(account string, s domain.Session, allowFile bool) error {
	blob, err := json.Marshal(s) // #nosec G117 -- intentional token persistence
	if err != nil {
		return fmt.Errorf("session: encode: %w", err)
	}
	setErr := keyring.Set(service, account, string(blob))
	if setErr == nil {
		return removeFile(account) // no plaintext copy once the keyring holds it
	}
	if allowFile {
		return saveFile(account, blob)
	}
	return fmt.Errorf("%w: %w", ErrNoSecretStore, setErr)
}

// Load returns the saved session for account; found=false with a nil error means
// nothing is stored (first run). The file fallback is consulted only when allowFile.
func Load(account string, allowFile bool) (domain.Session, bool, error) {
	blob, err := keyring.Get(service, account)
	switch {
	case err == nil:
		return decode([]byte(blob))
	case errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrUnsupportedPlatform):
	default:
		// A locked or unreachable keyring must not look like a first run, or the
		// user is told to log in again over a session that was fine.
		if allowFile {
			if s, found, ferr := readFile(sessionPath(account)); ferr == nil && found {
				return s, found, nil
			}
		}
		return domain.Session{}, false, fmt.Errorf(
			"session: the OS secret store could not be read (is the keyring unlocked?): %w", err)
	}
	if allowFile {
		return readFile(sessionPath(account))
	}
	return domain.Session{}, false, nil
}

// ErrCorruptPickleKey is a keyring entry that is not a pickle key. It is never
// replaced: a new key cannot open the crypto store the old one encrypted.
var ErrCorruptPickleKey = errors.New("session: the pickle key in the keyring is corrupt")

// LoadOrCreatePickleKey returns the stable key encrypting the E2EE crypto store,
// generating it on first use. It lives only in the keyring; without one it returns
// ErrNoSecretStore, and for an entry that is not a key, ErrCorruptPickleKey.
func LoadOrCreatePickleKey(account string) ([]byte, error) {
	acct := account + "|pickle"
	switch existing, err := keyring.Get(service, acct); {
	case err == nil:
		key, derr := base64.StdEncoding.DecodeString(existing)
		if derr == nil && len(key) == pickleKeyLen {
			return key, nil
		}
		return nil, fmt.Errorf("%w (service %q, account %q): restore it, or delete it together "+
			"with the crypto store, log in again and restore your keys from backup", ErrCorruptPickleKey, service, acct)
	case errors.Is(err, keyring.ErrNotFound):
	default:
		return nil, ErrNoSecretStore
	}
	key := make([]byte, pickleKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("session: generate pickle key: %w", err)
	}
	if err := keyring.Set(service, acct, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, ErrNoSecretStore
	}
	return key, nil
}

// Secret reads a named secret (e.g. a model API key). Deliberately not scoped to a
// Matrix account: the key belongs to the person. A missing entry is "" and no error.
func Secret(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	value, err := keyring.Get(service, secretAccount(ref))
	switch {
	case err == nil:
		return value, nil
	case errors.Is(err, keyring.ErrNotFound):
		return "", nil
	default:
		return "", ErrNoSecretStore
	}
}

// StoreSecret writes a named secret; an empty value is refused (that is a delete).
func StoreSecret(ref, value string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || value == "" {
		return errEmptySecret
	}
	if err := keyring.Set(service, secretAccount(ref), value); err != nil {
		return ErrNoSecretStore
	}
	return nil
}

// DeleteSecret removes a named secret.
func DeleteSecret(ref string) error {
	if err := keyring.Delete(service, secretAccount(ref)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("session: delete secret: %w", err)
	}
	return nil
}

func secretAccount(ref string) string { return "secret|" + ref }

func decode(blob []byte) (domain.Session, bool, error) {
	var s domain.Session
	if err := json.Unmarshal(blob, &s); err != nil {
		return domain.Session{}, false, fmt.Errorf("session: decode: %w", err)
	}
	return s, true, nil
}

// readFile reads one fallback file, refusing one that others can read.
func readFile(path string) (domain.Session, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("session: stat %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&^ownerOnly != 0 {
		return domain.Session{}, false, fmt.Errorf(
			"session: %s is group/other-accessible (%#o); it holds an access token — run: chmod 600 %s",
			path, perm, path)
	}
	blob, err := os.ReadFile(path) // #nosec G304 -- the session file, mode-checked above
	if err != nil {
		return domain.Session{}, false, fmt.Errorf("session: read %s: %w", path, err)
	}
	return decode(blob)
}

// saveFile writes the fallback file via temp file + sync + rename, so a failed save
// never destroys the previous (unrecoverable) token.
func saveFile(account string, blob []byte) error {
	path := sessionPath(account)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("session: mktemp beside %s: %w", path, err)
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
		return fmt.Errorf("session: write %s: %w", path, err)
	}
	return nil
}

func removeFile(account string) error {
	path := sessionPath(account)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("session: remove %s: %w", path, err)
	}
	return nil
}
