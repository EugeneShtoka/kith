package gmessages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

// Secrets is the OS secret store, via the daemon, which owns the keyring entries.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Session is a paired phone: the phone's ID and libgm's pairing (the keys, the
// browser and phone devices, the auth token, and for a Google-account pairing the
// account's cookies). It grants full access, so it lives only in the OS secret store.
type Session struct {
	// PhoneID is the paired phone's ID: the account part of every room and message ID.
	PhoneID string `json:"phone_id"`
	// Auth is the pairing as libgm keeps it; its token and cookies change as the
	// connection runs, and are saved again when they do.
	Auth *libgm.AuthData `json:"auth"`
}

// errIncomplete is a session missing a part the pairing needs.
var errIncomplete = errors.New("gmessages: a session needs the phone's ID and a finished pairing")

// errUnusable is a stored session that cannot be used: the account's problem, not the
// store's.
var errUnusable = errors.New("unusable")

func (s *Session) valid() error {
	if s.PhoneID == "" || s.Auth == nil || s.Auth.Browser == nil || len(s.Auth.TachyonAuthToken) == 0 {
		return errIncomplete
	}
	return nil
}

// sessionRef is the secret name for an account's session.
func sessionRef(account string) string { return "gmessages|" + account }

// loadSession reads an account's session; ok is false when not paired.
func loadSession(secrets Secrets, account string) (*Session, bool, error) {
	blob, ok, err := secrets.Secret(sessionRef(account))
	if err != nil || !ok {
		return nil, false, err //nolint:wrapcheck // the store's own error says which
	}
	var s Session
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		return nil, false, fmt.Errorf("gmessages: %s's session is %w: %w", account, errUnusable, err)
	}
	if err := s.valid(); err != nil {
		return nil, false, fmt.Errorf("gmessages: %s's session is %w: %w", account, errUnusable, err)
	}
	return &s, true, nil
}

// encode is the session as it is stored. The cookies are read under libgm's lock, as
// its requests write them; the rest changes only on the goroutine that reports it.
func (s *Session) encode() (string, error) {
	if err := s.valid(); err != nil {
		return "", err
	}
	s.Auth.CookiesLock.RLock()
	blob, err := json.Marshal(s)
	s.Auth.CookiesLock.RUnlock()
	if err != nil {
		return "", fmt.Errorf("gmessages: encode the session: %w", err)
	}
	return string(blob), nil
}

func saveSession(secrets Secrets, account string, s *Session) error {
	blob, err := s.encode()
	if err != nil {
		return err
	}
	return secrets.StoreSecret(sessionRef(account), blob) //nolint:wrapcheck // the store says why
}

// googleCookies are the Google account's cookies a pairing needs; googleOptional the
// one it takes when there.
var (
	googleCookies  = []string{"SID", "HSID", "SSID", "APISID", "SAPISID", "OSID"}
	googleOptional = []string{"__Secure-1PSIDTS"}
)

// Why a paste of cookies is refused, as the login says it.
var (
	errNoCookies = errors.New("that has no cookies: copy the value of the Cookie header of a request to messages.google.com")
	errMissing   = errors.New("those cookies lack some a pairing needs")
)

// parseCookies reads the Google cookies a login was given: the Cookie header's value,
// or the whole header line. Only the ones a pairing uses are kept.
func parseCookies(paste string) (map[string]string, error) {
	paste = strings.TrimSpace(paste)
	if name, value, ok := strings.Cut(paste, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "cookie") {
		paste = strings.TrimSpace(value)
	}
	if len(paste) >= 2 && (paste[0] == '\'' || paste[0] == '"') && paste[len(paste)-1] == paste[0] {
		paste = paste[1 : len(paste)-1]
	}
	if paste == "" {
		return nil, errNoCookies
	}
	parsed, err := http.ParseCookie(paste)
	if err != nil {
		return nil, fmt.Errorf("%w (%w)", errNoCookies, err)
	}
	out := map[string]string{}
	for _, c := range parsed {
		if slices.Contains(googleCookies, c.Name) || slices.Contains(googleOptional, c.Name) {
			out[c.Name] = c.Value
		}
	}
	var missing []string
	for _, name := range googleCookies {
		if out[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", errMissing, strings.Join(missing, ", "))
	}
	return out, nil
}
