package keystore

import (
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

// defaultService is the default keyring service; secrets under it are found from any.
const defaultService = "kith"

// SecretAccount is the keyring account a named secret (ref) is kept under.
func SecretAccount(ref string) string { return "secret|" + ref }

// Secret reads a named secret (an API key, a user command's) under service, falling back
// to the default service; not scoped to an instance. A missing entry is "" and no error.
// Every client reading one (the TUI, the desktop's relay running a command) reads it so.
func Secret(service, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	for _, svc := range []string{service, defaultService} {
		value, err := Get(svc, SecretAccount(ref))
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
