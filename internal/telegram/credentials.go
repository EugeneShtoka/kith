package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Secrets is the daemon's OS keyring as a name → value store.
type Secrets interface {
	// Secret is the value kept under ref; ok false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Credentials are one account's login: app, session and user. The session holds the
// auth key, which grants full account access; it lives only in the OS keyring.
type Credentials struct {
	App App `json:"app"`
	// Session is the client's session as gotd keeps it.
	Session []byte `json:"session"`
	// User is the account's user ID, the account part of its room and message IDs.
	User int64 `json:"user"`
}

// errIncomplete is credentials missing a part a connection needs.
var errIncomplete = errors.New("telegram: credentials need an app, a session and a user")

// errUnusable is stored credentials that can't be used: the account's problem, not
// the store's.
var errUnusable = errors.New("unusable")

// valid reports whether every part is there.
func (c Credentials) valid() error {
	if !c.App.Valid() || len(c.Session) == 0 || c.User == 0 {
		return errIncomplete
	}
	return nil
}

// credentialsRef keys credentials by phone number, so a config rename keeps them.
func credentialsRef(digits string) string { return "telegram|" + digits }

// loadCredentials is an account's credentials; ok false when it is not logged in.
func loadCredentials(secrets Secrets, digits string) (Credentials, bool, error) {
	blob, ok, err := secrets.Secret(credentialsRef(digits))
	if err != nil || !ok {
		return Credentials{}, false, err //nolint:wrapcheck // the store's own error says which
	}
	var c Credentials
	if err := json.Unmarshal([]byte(blob), &c); err != nil {
		return Credentials{}, false, fmt.Errorf("telegram: +%s's credentials are %w: %w", digits, errUnusable, err)
	}
	if err := c.valid(); err != nil {
		return Credentials{}, false, fmt.Errorf("telegram: +%s's credentials are %w: %w", digits, errUnusable, err)
	}
	return c, true, nil
}

// saveCredentials stores an account's credentials.
func saveCredentials(secrets Secrets, digits string, c Credentials) error {
	if err := c.valid(); err != nil {
		return err
	}
	blob, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("telegram: encode +%s's credentials: %w", digits, err)
	}
	if err := secrets.StoreSecret(credentialsRef(digits), string(blob)); err != nil {
		return fmt.Errorf("telegram: keep +%s's credentials: %w", digits, err)
	}
	return nil
}
