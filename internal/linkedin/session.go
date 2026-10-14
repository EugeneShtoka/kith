package linkedin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"
)

// Secrets is the OS secret store, via the daemon, which owns the keyring entries.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Session is a signed-in LinkedIn account: the browser's cookies for www.linkedin.com
// (they rotate, and are saved again when they do) and the two headers the web app sends
// with them. It grants full access, so it lives only in the OS secret store.
type Session struct {
	// ProfileID is the account's profile ID ("ACoAA…"): the account part of every room
	// and message ID.
	ProfileID string `json:"profile_id"`
	// Cookies are the browser's cookies; li_at and JSESSIONID are the session.
	Cookies *linkedingo.StringCookieJar `json:"cookies"`
	// Track and PageInstance are the X-LI-Track and X-LI-Page-Instance headers the
	// browser sent, when the paste had them; linkedingo's own otherwise.
	Track        string `json:"x_li_track,omitempty"`
	PageInstance string `json:"x_li_page_instance,omitempty"`
}

// The cookies a session cannot do without.
const (
	cookieSession = "li_at"
	cookieCSRF    = linkedingo.LinkedInCookieJSESSIONID
)

// errIncomplete is a session missing a part LinkedIn needs.
var errIncomplete = errors.New("linkedin: a session needs a profile ID and the li_at and JSESSIONID cookies")

// errUnusable is a stored session that cannot be used: the account's problem, not the
// store's.
var errUnusable = errors.New("unusable")

func (s Session) valid() error {
	if s.ProfileID == "" || !hasSession(s.Cookies) {
		return errIncomplete
	}
	return nil
}

// hasSession reports whether jar holds both session cookies.
func hasSession(jar *linkedingo.StringCookieJar) bool {
	return jar != nil && jar.GetCookie(cookieSession) != "" && jar.GetCookie(cookieCSRF) != ""
}

// sessionRef is the secret name for an account's session.
func sessionRef(account string) string { return "linkedin|" + account }

// loadSession reads an account's session; ok is false when not signed in.
func loadSession(secrets Secrets, account string) (Session, bool, error) {
	blob, ok, err := secrets.Secret(sessionRef(account))
	if err != nil || !ok {
		return Session{}, false, err //nolint:wrapcheck // the store's own error says which
	}
	var s Session
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		return Session{}, false, fmt.Errorf("linkedin: %s's session is %w: %w", account, errUnusable, err)
	}
	if err := s.valid(); err != nil {
		return Session{}, false, fmt.Errorf("linkedin: %s's session is %w: %w", account, errUnusable, err)
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
		return "", fmt.Errorf("linkedin: encode the session: %w", err)
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

// pasted is what a login's paste gave: the cookies, and the headers when it had them.
type pasted struct {
	cookies             *linkedingo.StringCookieJar
	track, pageInstance string
}

var (
	// gState is Google's sign-in cookie, whose JSON value the cookie parser refuses
	// (the bridge drops it too).
	gState = regexp.MustCompile(`g_state=\{[^}]*\};?\s*`)
	// curlHeader is one -H '<name>: <value>' of a "Copy as cURL" line; curlCookies is
	// its -b '<cookies>'.
	curlHeader  = regexp.MustCompile(`(?i)(?:-H|--header)\s+(?:\$?'([^']*)'|"([^"]*)")`)
	curlCookies = regexp.MustCompile(`(?i)(?:-b|--cookie)\s+(?:\$?'([^']*)'|"([^"]*)")`)
)

// Why a paste is refused, as the login says it.
var (
	errNoCookies = errors.New("that has no cookies: copy the value of the Cookie header of a request to www.linkedin.com")
	errNoSession = errors.New("those cookies have no li_at or JSESSIONID: copy them from a browser signed in to LinkedIn")
)

// parsePaste reads the cookies a login was given: the Cookie header's value, the whole
// header line, or a "Copy as cURL" command on one line (which also brings the X-LI
// headers).
func parsePaste(paste string) (pasted, error) {
	paste = strings.TrimSpace(paste)
	var got pasted
	cookies := paste
	if strings.HasPrefix(strings.ToLower(paste), "curl ") {
		cookies = ""
		for _, m := range curlHeader.FindAllStringSubmatch(paste, -1) {
			name, value, _ := strings.Cut(m[1]+m[2], ":")
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "cookie":
				cookies = value
			case "x-li-track":
				got.track = value
			case "x-li-page-instance":
				got.pageInstance = value
			}
		}
		if m := curlCookies.FindStringSubmatch(paste); m != nil {
			cookies = m[1] + m[2]
		}
	} else if name, value, ok := strings.Cut(paste, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "cookie") {
		cookies = value
	}
	cookies = unquote(strings.TrimSpace(cookies))
	cookies = gState.ReplaceAllString(cookies, "")
	if cookies == "" {
		return pasted{}, errNoCookies
	}
	jar, err := linkedingo.NewJarFromCookieHeader(cookies)
	if err != nil {
		return pasted{}, fmt.Errorf("%w (%w)", errNoCookies, err)
	}
	if !hasSession(jar) {
		return pasted{}, errNoSession
	}
	got.cookies = jar
	return got, nil
}

// unquote is s without a pair of quotes around it; a quote that ends the last cookie's
// own value (JSESSIONID="ajax:…") stays.
func unquote(s string) string {
	for _, q := range []string{"'", `"`} {
		if len(s) >= 2 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			return s[1 : len(s)-1]
		}
	}
	return s
}
