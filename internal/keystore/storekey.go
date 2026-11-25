package keystore

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// storeKeyLen is a store key's length in bytes, as sealeddb takes it.
const storeKeyLen = 32

// LoadOrCreateKey returns the 32-byte key in the keyring entry (service, acct), making
// and keeping one when there is none. One that is not a key is corrupt, reported with
// advice and never replaced; no keyring is ErrNoSecretStore, never a key kept elsewhere.
// A key is made only when the keyring answered that there is none: one that did not
// answer is ErrKeyringNotAnswering. A key made but not yet kept is not used; if the
// keyring keeps it later, the next call reads it.
func LoadOrCreateKey(service, acct string, corrupt error, advice string) ([]byte, error) {
	switch existing, err := Get(service, acct); {
	case err == nil:
		key, derr := base64.StdEncoding.DecodeString(existing)
		if derr == nil && len(key) == storeKeyLen {
			return key, nil
		}
		return nil, fmt.Errorf("%w (service %q, account %q): %s", corrupt, service, acct, advice)
	case errors.Is(err, keyring.ErrNotFound):
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	default:
		return nil, ErrNoSecretStore
	}
	key := make([]byte, storeKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("keystore: generate a key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	kept, err := Set(service, acct, encoded)
	switch {
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	case err != nil:
		return nil, ErrNoSecretStore
	}
	if key, err = base64.StdEncoding.DecodeString(kept); err != nil {
		return nil, fmt.Errorf("keystore: the key just kept: %w", err)
	}
	return key, nil
}
