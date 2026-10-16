package meta

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

// Secrets is the OS secret store, via the daemon, which owns the keyring entries.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Session is a signed-in Meta account: the web cookies the login ended with (they
// rotate, and are saved again when they do) and the app session some logins carry. It
// grants full access, so it lives only in the OS secret store. The password never does.
type Session struct {
	// MetaID is the Meta user ID: the account part of every room and message ID.
	MetaID int64 `json:"meta_id"`
	// Platform is the app the login was made as (Messenger Lite on iOS).
	Platform types.Platform `json:"platform"`
	// Cookies are the session cookies by name (c_user, xs, datr, …).
	Cookies map[string]string `json:"cookies"`
	// Native is the app session an Android login also carries; nil for iOS.
	Native *types.NativeSession `json:"native,omitempty"`
	// WADevice is the number of the device kith registered for encrypted chats, in the
	// Messenger store; 0 before one is.
	WADevice uint16 `json:"wa_device,omitempty"`
}

// errIncomplete is a session missing a part Meta needs.
var errIncomplete = errors.New("meta: a session needs a Meta user ID, an app and its cookies")

// errUnusable is a stored session that cannot be used: the account's problem, not the
// store's.
var errUnusable = errors.New("unusable")

func (s Session) valid() error {
	if s.MetaID <= 0 || s.Platform == types.Unset || len(s.cookies().GetMissingCookieNames()) > 0 {
		return errIncomplete
	}
	return nil
}

// cookies is the session's cookies as messagix holds them.
func (s Session) cookies() *cookies.Cookies {
	c := &cookies.Cookies{Platform: s.Platform}
	values := make(map[cookies.MetaCookieName]string, len(s.Cookies))
	for name, value := range s.Cookies {
		values[cookies.MetaCookieName(name)] = value
	}
	c.UpdateValues(values)
	return c
}

// withCookies is the session holding c's current values.
func (s Session) withCookies(c *cookies.Cookies) Session {
	all := c.GetAll()
	s.Cookies = make(map[string]string, len(all))
	for name, value := range all {
		s.Cookies[string(name)] = value
	}
	return s
}

// sessionRef is the secret name for an account's session.
func sessionRef(account string) string { return "messenger|" + account }

// loadSession reads an account's session; ok is false when not signed in.
func loadSession(secrets Secrets, account string) (Session, bool, error) {
	blob, ok, err := secrets.Secret(sessionRef(account))
	if err != nil || !ok {
		return Session{}, false, err //nolint:wrapcheck // the store's own error says which
	}
	var s Session
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		return Session{}, false, fmt.Errorf("meta: %s's session is %w: %w", account, errUnusable, err)
	}
	if err := s.valid(); err != nil {
		return Session{}, false, fmt.Errorf("meta: %s's session is %w: %w", account, errUnusable, err)
	}
	return s, true, nil
}

// encode is the session as it is stored.
func (s Session) encode() (string, error) {
	if err := s.valid(); err != nil {
		return "", err
	}
	blob, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("meta: encode the session: %w", err)
	}
	return string(blob), nil
}

func saveSession(secrets Secrets, account string, s Session) error {
	blob, err := s.encode()
	if err != nil {
		return err
	}
	return secrets.StoreSecret(sessionRef(account), blob) //nolint:wrapcheck // the store says why
}
