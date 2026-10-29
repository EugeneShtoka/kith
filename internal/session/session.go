// Package session persists login credentials in the OS secret store (go-keyring, no
// cgo). Without one, the token is persisted only if the caller opts into a per-account
// 0600 file in the XDG state dir, removed once the keyring works again.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
)

const (
	// legacyService is the default keyring service; secrets under it are found from any.
	legacyService             = "kith"
	pickleKeyLen              = 32
	ownerOnly     fs.FileMode = 0o600
)

// Store locates one instance's session and encryption key: keyring service, account
// and fallback file.
type Store = keystore.Store

// MatrixStore is where the instance keeps the session and encryption key of the Matrix
// account whose key (domain.AccountKey) is key: each account its own, so two accounts
// or two instances never share a device.
func MatrixStore(storage domain.Storage, key string) Store {
	return Store{Service: storage.KeyringService, Account: storage.Instance + "|matrix|" + key, File: storage.MatrixSessionFile(key)}
}

// ErrNoSecretStore means no OS secret store and no file fallback; a warning, not fatal.
var ErrNoSecretStore = keystore.ErrNoSecretStore

// ErrKeyringNotAnswering is a keyring that did not answer in time; it is not "no key".
var ErrKeyringNotAnswering = keystore.ErrKeyringNotAnswering

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
		return keystore.RemoveFile(st.File) // no plaintext copy once the keyring holds it
	}
	if allowFile {
		return keystore.SaveFile(st.File, blob)
	}
	return fmt.Errorf("%w: %w", ErrNoSecretStore, setErr)
}

// Load returns the saved session; found=false with nil error means a first run.
func Load(st Store, allowFile bool) (domain.Session, bool, error) {
	blob, err := keystore.Get(st.Service, st.Account)
	switch {
	case err == nil:
		return decode([]byte(blob))
	case errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrUnsupportedPlatform):
	default:
		// A locked or unreachable keyring is not a first run.
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

// Forget removes the saved session from the OS secret store and the file fallback.
// The encryption key stays. With allowFile, an unreachable secret store is no failure:
// Save falls back to the file only when it cannot reach the store.
func Forget(st Store, allowFile bool) error {
	if err := keystore.RemoveFile(st.File); err != nil {
		return err
	}
	err := keyring.Delete(st.Service, st.Account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) || errors.Is(err, keyring.ErrUnsupportedPlatform) || allowFile {
		return nil
	}
	return fmt.Errorf("session: forget the session: %w", err)
}

// ErrCorruptPickleKey is a keyring entry that is not a pickle key. It is never
// replaced: a new key cannot open the crypto store the old one encrypted.
var ErrCorruptPickleKey = errors.New("session: the pickle key in the keyring is corrupt")

// LoadOrCreatePickleKey returns the E2EE crypto store key, generating it on first use.
// It lives only in the keyring.
func LoadOrCreatePickleKey(st Store) ([]byte, error) {
	return loadOrCreateKey(st.Service, st.Account+"|pickle", ErrCorruptPickleKey,
		"restore it, or delete it together with the crypto store, log in again and restore your keys from backup")
}

// ErrCorruptStoreKey is a keyring entry that is not a network store's key. It is never
// replaced: a new key cannot open the store the old one encrypted.
var ErrCorruptStoreKey = errors.New("session: a network store's key in the keyring is corrupt")

// LoadOrCreateStoreKey returns the key a network's own store (name: "whatsapp",
// "messenger") of instance is encrypted with, generating it on first use. It lives
// only in the keyring; without one there is no key, and the store stays closed.
func LoadOrCreateStoreKey(service, instance, name string) ([]byte, error) {
	return loadOrCreateKey(service, instance+"|"+name+"-store", ErrCorruptStoreKey,
		"restore it, or delete it together with the store and link the account again")
}

// loadOrCreateKey returns the 32-byte key in the keyring entry (service, acct), making
// and keeping one when there is none. One that is not a key is corrupt, reported with
// advice and never replaced; no keyring is ErrNoSecretStore, never a key kept elsewhere.
// A key is made only when the keyring answered that there is none: one that did not
// answer is ErrKeyringNotAnswering. A key made but not yet kept is not used; if the
// keyring keeps it later, the next call reads it.
func loadOrCreateKey(service, acct string, corrupt error, advice string) ([]byte, error) {
	switch existing, err := keystore.Get(service, acct); {
	case err == nil:
		key, derr := base64.StdEncoding.DecodeString(existing)
		if derr == nil && len(key) == pickleKeyLen {
			return key, nil
		}
		return nil, fmt.Errorf("%w (service %q, account %q): %s", corrupt, service, acct, advice)
	case errors.Is(err, keyring.ErrNotFound):
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	default:
		return nil, ErrNoSecretStore
	}
	key := make([]byte, pickleKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("session: generate a key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	kept, err := keystore.Set(service, acct, encoded)
	switch {
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	case err != nil:
		return nil, ErrNoSecretStore
	}
	if key, err = base64.StdEncoding.DecodeString(kept); err != nil {
		return nil, fmt.Errorf("session: the key just kept: %w", err)
	}
	return key, nil
}

// Secret reads a named secret (e.g. an API key) under service, falling back to the
// default service; not scoped to an instance. A missing entry is "" and no error.
func Secret(service, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	for _, svc := range []string{service, legacyService} {
		value, err := keystore.Get(svc, secretAccount(ref))
		switch {
		case err == nil:
			return value, nil
		case errors.Is(err, keyring.ErrNotFound):
		case errors.Is(err, ErrKeyringNotAnswering):
			return "", err
		default:
			return "", ErrNoSecretStore
		}
	}
	return "", nil
}

// StoreSecret writes a named secret under service; an empty value is refused.
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

// readFile reads the fallback file, refusing one others can read.
func readFile(path string) (domain.Session, bool, error) {
	blob, found, err := keystore.ReadOwnerOnly(path, "an access token")
	if err != nil || !found {
		return domain.Session{}, false, err
	}
	return decode(blob)
}
