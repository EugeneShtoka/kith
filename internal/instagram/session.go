package instagram

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

// Secrets is the system keyring, where each account's session is kept.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Session is a signed-in account: its instagram.com cookies (they rotate as it is
// used, and are kept again when they do), who it is, and how far its live events got.
type Session struct {
	Cookies *cookies.Cookies `json:"cookies"`
	// Self is the account's own ID (its Facebook-side ID, as instameow names it).
	Self string `json:"self"`
	// SeqID and SeqTS resume live events where they stopped; 0 starts from the inbox.
	SeqID int64     `json:"seq_id,omitempty"`
	SeqTS time.Time `json:"seq_ts,omitzero"`
}

func sessionRef(account string) string { return "instagram|" + account }

// errUnusable is a kept session that no longer reads as one.
var errUnusable = errors.New("instagram: the kept session is unusable; log in again")

// loadSession reads an account's session; ok is false when it is not signed in.
func loadSession(secrets Secrets, account string) (*Session, bool, error) {
	blob, ok, err := secrets.Secret(sessionRef(account))
	if err != nil {
		return nil, false, fmt.Errorf("instagram: read the session of %s: %w", account, err)
	}
	if !ok || blob == "" {
		return nil, false, nil
	}
	s := &Session{}
	if err := json.Unmarshal([]byte(blob), s); err != nil || s.Cookies == nil || s.Self == "" {
		return nil, false, errUnusable
	}
	s.Cookies.Platform = types.Instagram
	return s, true, nil
}

func saveSession(secrets Secrets, account string, s *Session) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("instagram: keep the session: %w", err)
	}
	return secrets.StoreSecret(sessionRef(account), string(blob)) //nolint:wrapcheck // the store says why
}

// errNoCookies and errMissingCookies say why a paste is no session.
var (
	errNoCookies      = errors.New("no cookies in that: paste the Cookie header of a request to www.instagram.com, or a whole \"Copy as cURL\"")
	errMissingCookies = errors.New("those cookies are not a signed-in instagram.com session")
)

// parseCookies reads cookies from a paste: a Cookie header's value (with or without
// "Cookie:"), or a cURL command, whose cookies are its -b/--cookie argument or its
// cookie header (Chrome's "Copy as cURL" uses -b).
func parseCookies(paste string) (*cookies.Cookies, error) {
	c := &cookies.Cookies{Platform: types.Instagram}
	c.UpdateValues(nil)
	found := false
	for _, list := range cookieLists(paste) {
		for part := range strings.SplitSeq(list, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && name != "" && value != "" {
				c.Set(cookies.MetaCookieName(name), value)
				found = true
			}
		}
	}
	if !found {
		return nil, errNoCookies
	}
	if missing := c.GetMissingCookieNames(); len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, m := range missing {
			names = append(names, string(m))
		}
		return nil, fmt.Errorf("%w (missing %s)", errMissingCookies, strings.Join(names, ", "))
	}
	return c, nil
}

// cookieLists is the cookie lists in a paste: a cURL's -b/--cookie arguments and cookie
// headers, else the whole paste as a Cookie header's value.
func cookieLists(paste string) []string {
	var out []string
	lower := strings.ToLower(paste)
	for _, marker := range []string{"-b '", `-b "`, "--cookie '", `--cookie "`, "cookie: "} {
		for at := 0; ; {
			i := strings.Index(lower[at:], marker)
			if i < 0 {
				break
			}
			start := at + i + len(marker)
			end := strings.IndexAny(paste[start:], "'\"\n")
			if end < 0 {
				end = len(paste) - start
			}
			out = append(out, paste[start:start+end])
			at = start + end
		}
	}
	if len(out) == 0 {
		trimmed := strings.TrimSpace(paste)
		if len(trimmed) >= len("cookie:") && strings.EqualFold(trimmed[:len("cookie:")], "cookie:") {
			trimmed = trimmed[len("cookie:"):]
		}
		out = []string{trimmed}
	}
	return out
}
