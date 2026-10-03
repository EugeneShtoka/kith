package slack

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

// Credentials are one workspace's session as the Slack web client holds it: the
// xoxc token and the `d` cookie it is only good together with, and who and where
// they sign in as. Whoever has them can read and write as the person, so they live
// in the OS secret store alone.
type Credentials struct {
	// Team is the workspace's ID: the account part of every room and message ID.
	Team string `json:"team"`
	// User is the person's user ID in the workspace.
	User   string `json:"user"`
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
}

// errIncomplete is credentials missing a part Slack needs.
var errIncomplete = errors.New("slack: credentials need a team, a user, a token and a cookie")

// errUnusable is credentials that were kept but cannot be used: one account's
// problem, not the store's.
var errUnusable = errors.New("unusable")

// valid reports whether every part is there.
func (c Credentials) valid() error {
	if c.Team == "" || c.User == "" || c.Token == "" || c.Cookie == "" {
		return errIncomplete
	}
	return nil
}

// credentialsRef is where an account's credentials are kept, by its configured name.
func credentialsRef(account string) string { return "slack|" + account }

// loadCredentials is an account's credentials; ok false when it is not signed in.
func loadCredentials(secrets Secrets, account string) (Credentials, bool, error) {
	blob, ok, err := secrets.Secret(credentialsRef(account))
	if err != nil || !ok {
		return Credentials{}, false, err //nolint:wrapcheck // the store's own error says which
	}
	var c Credentials
	if err := json.Unmarshal([]byte(blob), &c); err != nil {
		return Credentials{}, false, fmt.Errorf("slack: %s's credentials are %w: %w", account, errUnusable, err)
	}
	if err := c.valid(); err != nil {
		return Credentials{}, false, fmt.Errorf("slack: %s's credentials are %w: %w", account, errUnusable, err)
	}
	return c, true, nil
}
