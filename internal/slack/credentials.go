package slack

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Secrets is the OS secret store, via the daemon, which owns the keyring entries.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Credentials are a web-client session: the xoxc token, the `d` cookie it only works
// with, and the team and user. They grant full access, so they live only in the OS
// secret store.
type Credentials struct {
	// Team is the workspace ID: the account part of every room and message ID.
	Team string `json:"team"`
	// User is the person's user ID in the workspace.
	User   string `json:"user"`
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
}

// errIncomplete is credentials missing a part Slack needs.
var errIncomplete = errors.New("slack: credentials need a team, a user, a token and a cookie")

// errUnusable is stored credentials that cannot be used: the account's problem, not
// the store's.
var errUnusable = errors.New("unusable")

func (c Credentials) valid() error {
	if c.Team == "" || c.User == "" || c.Token == "" || c.Cookie == "" {
		return errIncomplete
	}
	return nil
}

// credentialsRef is the secret name for an account's credentials.
func credentialsRef(account string) string { return "slack|" + account }

// loadCredentials reads an account's credentials; ok is false when not signed in.
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

func saveCredentials(secrets Secrets, account string, c Credentials) error {
	if err := c.valid(); err != nil {
		return err
	}
	blob, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("slack: encode the credentials: %w", err)
	}
	return secrets.StoreSecret(credentialsRef(account), string(blob)) //nolint:wrapcheck // the store says why
}
