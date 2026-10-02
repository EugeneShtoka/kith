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

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	// legacyService is the keyring service before it was configurable; secrets
	// kept under it are still found.
	legacyService             = "kith"
	pickleKeyLen              = 32
	ownerOnly     fs.FileMode = 0o600
)

// Store is where one instance's session and encryption key are kept: a keyring
// service and account, and the fallback file.
type Store struct {
	Service string
	Account string
	File    string
}

// StoreFor is the instance's store. An install from before instances (its instance
// is the name made from the Matrix account) keeps the keyring entries it always had,
// under the account itself, so nothing moves; any other instance has its own, and
// never reads another's: a second instance must not take over the first one's
// Matrix device.
func StoreFor(storage domain.Storage, user string) Store {
	account := storage.Instance
	if user != "" && storage.Instance == domain.AccountKey(user) {
		account = user
	}
	return Store{Service: storage.KeyringService, Account: account, File: storage.SessionFile()}
}

// ErrNoSecretStore is returned when no OS secret store is usable and the file
// fallback was not opted into. It is a warning to relay, not a fatal error.
var ErrNoSecretStore = errors.New(
	"session: no OS secret store available; token not persisted (set allow_token_file to persist to a 0600 file)")

var errEmptySecret = errors.New("session: a secret needs a name and a value")

// Save stores the session in the OS secret store, or in the 0600 file when the store
// is unavailable and allowFile is set.
func Save(st Store, s domain.Session, allowFile bool) error {
	blob, err := json.Marshal(s) // #nosec G117 -- intentional token persistence
	if err != nil {
		return fmt.Errorf("session: encode: %w", err)
	}
	setErr := keyring.Set(st.Service, st.Account, string(blob))
	if setErr == nil {
		return removeFile(st.File) // no plaintext copy once the keyring holds it
	}
	if allowFile {
		return saveFile(st.File, blob)
	}
	return fmt.Errorf("%w: %w", ErrNoSecretStore, setErr)
}

// Load returns the saved session; found=false with a nil error means nothing is
// stored (first run). The file fallback is consulted only when allowFile.
func Load(st Store, allowFile bool) (domain.Session, bool, error) {
	blob, err := keyring.Get(st.Service, st.Account)
	switch {
	case err == nil:
		return decode([]byte(blob))
	case errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrUnsupportedPlatform):
	default:
		// A locked or unreachable keyring must not look like a first run, or the
		// user is told to log in again over a session that was fine.
		if allowFile {
			if s, found, ferr := readFile(st.File); ferr == nil && found {
				return s, found, nil
			}
		}
		return domain.Session{}, false, fmt.Errorf(
			"session: the OS secret store could not be read (is the keyring unlocked?): %w", err)
	}
	if allowFile {
		return readFile(st.File)
	}
	return domain.Session{}, false, nil
}

// ErrCorruptPickleKey is a keyring entry that is not a pickle key. It is never
// replaced: a new key cannot open the crypto store the old one encrypted.
var ErrCorruptPickleKey = errors.New("session: the pickle key in the keyring is corrupt")

// LoadOrCreatePickleKey returns the stable key encrypting the E2EE crypto store,
// generating it on first use. It lives only in the keyring; without one it returns
// ErrNoSecretStore, and for an entry that is not a key, ErrCorruptPickleKey.
func LoadOrCreatePickleKey(st Store) ([]byte, error) {
	service, acct := st.Service, st.Account+"|pickle"
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

// Secret reads a named secret (e.g. a model API key) under service. Deliberately not
// scoped to an instance or an account: the key belongs to the person, so one kept
// under the default service is found from any. A missing entry is "" and no error.
func Secret(service, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	for _, svc := range []string{service, legacyService} {
		value, err := keyring.Get(svc, secretAccount(ref))
		switch {
		case err == nil:
			return value, nil
		case errors.Is(err, keyring.ErrNotFound):
		default:
			return "", ErrNoSecretStore
		}
	}
	return "", nil
}

// StoreSecret writes a named secret under service; an empty value is refused (that
// is a delete).
func StoreSecret(service, ref, value string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || value == "" {
		return errEmptySecret
	}
	if err := keyring.Set(service, secretAccount(ref), value); err != nil {
		return ErrNoSecretStore
	}
	return nil
}

// DeleteSecret removes a named secret from service.
func DeleteSecret(service, ref string) error {
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
func saveFile(path string, blob []byte) error {
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

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("session: remove %s: %w", path, err)
	}
	return nil
}
