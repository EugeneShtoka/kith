package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Secrets is where credentials are kept: the OS secret store, through the daemon (it
// owns the keyring entries; this package needs only a name → value store).
type Secrets interface {
	// Secret is the value kept under ref; ok false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Credentials are one account's login: the app it logged in through, the session
// Telegram gave it (its auth key — whoever has it reads and writes as the person), and
// whom it is. They live in the OS secret store alone.
type Credentials struct {
	App App `json:"app"`
	// Session is the client's session as gotd keeps it.
	Session []byte `json:"session"`
	// User is the account's own user ID: the account part of every room and message ID.
	User int64 `json:"user"`
}

// errIncomplete is credentials missing a part a connection needs.
var errIncomplete = errors.New("telegram: credentials need an app, a session and a user")

// errUnusable is credentials that were kept but cannot be used: one account's
// problem, not the store's.
var errUnusable = errors.New("unusable")

// valid reports whether every part is there.
func (c Credentials) valid() error {
	if !c.App.Valid() || len(c.Session) == 0 || c.User == 0 {
		return errIncomplete
	}
	return nil
}

// credentialsRef is where an account's credentials are kept: by its number, which a
// rename in the config does not change.
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
