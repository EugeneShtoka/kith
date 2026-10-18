package instagram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memSecrets is a keyring in memory.
type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func newSecrets() *memSecrets { return &memSecrets{m: map[string]string{}} }

func (s *memSecrets) Secret(ref string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	return v, ok, nil
}

func (s *memSecrets) StoreSecret(ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = value
	return nil
}

func (s *memSecrets) DeleteSecret(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}

// The cookies of a session, invented.
const header = "csrftoken=c; ds_user_id=101; sessionid=101%3Aabc%3A1; mid=m; ig_did=d; rur=r"

// A paste is taken as a Cookie header, with or without "Cookie:", or as a cURL with
// the cookies in -b (Chrome's "Copy as cURL") or in a cookie header (Firefox's).
func TestCookiesAreReadFromEveryPaste(t *testing.T) {
	t.Parallel()
	for name, paste := range map[string]string{
		"header":       header,
		"with Cookie:": "Cookie: " + header,
		"curl -b":      "curl 'https://www.instagram.com/direct/inbox/' \\\n  -H 'accept: */*' \\\n  -b '" + header + "' \\\n  -H 'x-ig-app-id: 1'",
		"curl -H":      "curl 'https://www.instagram.com/direct/inbox/' -H 'Accept: */*' -H 'Cookie: " + header + "'",
	} {
		c, err := parseCookies(paste)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if c.Get(cookies.IGCookieDSUserID) != "101" || c.Get(cookies.IGCookieSessionID) != "101%3Aabc%3A1" || c.Platform != types.Instagram {
			t.Errorf("%s: read %v", name, c)
		}
	}
}

// What is no session is refused, saying what is missing.
func TestAPasteThatIsNoSessionIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := parseCookies("hello"); !errors.Is(err, errNoCookies) {
		t.Errorf("no cookies = %v", err)
	}
	_, err := parseCookies("csrftoken=c; sessionid=s")
	if !errors.Is(err, errMissingCookies) || !strings.Contains(err.Error(), "ds_user_id") {
		t.Errorf("missing ds_user_id = %v", err)
	}
}

// A kept session reads back as it was kept; one that no longer reads is unusable, and
// none is not signed in.
func TestSessionsAreKept(t *testing.T) {
	t.Parallel()
	secrets := newSecrets()
	c, err := parseCookies(header)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if err = saveSession(secrets, "me", &Session{Cookies: c, Self: "17840000000000001", SeqID: 235, SeqTS: at}); err != nil {
		t.Fatal(err)
	}
	s, ok, err := loadSession(secrets, "me")
	if err != nil || !ok {
		t.Fatalf("load = %v, %v", ok, err)
	}
	if s.Self != "17840000000000001" || s.SeqID != 235 || !s.SeqTS.Equal(at) || s.Cookies.Get(cookies.IGCookieSessionID) != "101%3Aabc%3A1" {
		t.Errorf("loaded %+v", s)
	}
	if _, ok, err := loadSession(secrets, "nobody"); ok || err != nil {
		t.Errorf("no session = %v, %v", ok, err)
	}
	for name, blob := range map[string]string{
		"not json":   "{not json",
		"no cookies": `{"self":"1"}`,
		"no self":    `{"cookies":{}}`,
	} {
		secrets.m[sessionRef(name)] = blob
		if _, _, err := loadSession(secrets, name); !errors.Is(err, errUnusable) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func withAccounts(names ...string) config.Config {
	var cfg config.Config
	for _, n := range names {
		cfg.Instagram.Accounts = append(cfg.Instagram.Accounts, config.InstagramAccount{Name: n})
	}
	return cfg
}

// The config's accounts need names, each once.
func TestTheConfigIsChecked(t *testing.T) {
	t.Parallel()
	a := New(nil, newSecrets(), nil, nil)
	if err := a.CheckConfig(context.Background(), withAccounts("me", "work")); err != nil {
		t.Errorf("two accounts: %v", err)
	}
	if err := a.CheckConfig(context.Background(), withAccounts("me", "me")); err == nil {
		t.Error("a repeated name passed")
	}
	if err := a.CheckConfig(context.Background(), withAccounts("")); err == nil {
		t.Error("a nameless account passed")
	}
}

// An account with no session says how to sign in, once, and is no saved session; one
// with a session is.
func TestAccountsSayWhetherTheyAreSignedIn(t *testing.T) {
	t.Parallel()
	secrets := newSecrets()
	c, _ := parseCookies(header)
	if err := saveSession(secrets, "work", &Session{Cookies: c, Self: "2"}); err != nil {
		t.Fatal(err)
	}
	a := New(nil, secrets, nil, nil)
	var mu sync.Mutex
	var said []domain.AccountStatus
	a.OnStatus(func(s domain.AccountStatus) {
		mu.Lock()
		defer mu.Unlock()
		said = append(said, s)
	})
	a.UseConfig(context.Background(), withAccounts("me"))
	saved, err := a.SavedSessions(context.Background())
	if err != nil || len(saved) != 0 {
		t.Errorf("saved = %v, %v", saved, err)
	}
	a.UseConfig(context.Background(), withAccounts("me", "work"))
	if saved, _ = a.SavedSessions(context.Background()); !slices.Equal(saved, []string{"work"}) {
		t.Errorf("saved = %v", saved)
	}
	a.UseConfig(context.Background(), withAccounts("me"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(said)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.UseConfig(context.Background(), withAccounts("me"))
	cancel()
	<-done
	a.Stop()
	mu.Lock()
	defer mu.Unlock()
	if len(said) != 1 || said[0].Account != "me" || said[0].Phase != SignedOut || !strings.Contains(said[0].Detail, "kith login instagram me") {
		t.Errorf("said %+v, want one: not signed in", said)
	}
}

// A paste that is no session is asked for again, saying why; a new account is set up
// in the config first.
func TestLoginAsksAgainForCookiesThatAreNoSession(t *testing.T) {
	t.Parallel()
	a := New(nil, newSecrets(), nil, nil)
	talk := &apitest.Talk{
		Answers: map[string][]string{"name": {"me"}, "cookies": {"hello", "csrftoken=c; sessionid=s"}},
		OnConfigure: func(r api.LoginRecord) error {
			a.UseConfig(context.Background(), withAccounts(r.Values["name"]))
			return nil
		},
	}
	_, err := a.Login(context.Background(), "", talk)
	if !errors.Is(err, apitest.ErrNoAnswer) {
		t.Fatalf("login = %v, want it to ask a third time", err)
	}
	if len(talk.Written) != 1 || talk.Written[0].Table != "instagram.account" || talk.Account != "me" {
		t.Errorf("set up %+v as %q", talk.Written, talk.Account)
	}
	if len(talk.Notes) != 2 || !strings.Contains(talk.Notes[0], "no cookies") || !strings.Contains(talk.Notes[1], "ds_user_id") {
		t.Errorf("notes = %q", talk.Notes)
	}
	if _, err := a.Login(context.Background(), "nobody", talk); !errors.Is(err, ErrNoAccount) {
		t.Errorf("an unknown account = %v", err)
	}
}

// Instagram ending the session, or wanting a step only its website takes, ends the
// connection with a reason; anything else is tried again.
func TestWhatEndsASession(t *testing.T) {
	t.Parallel()
	for err, over := range map[error]bool{
		httpclient.ErrTokenInvalidated:                                    true,
		fmt.Errorf("load: %w", httpclient.ErrConsentRequired):             true,
		httpclient.ErrCheckpointRequired:                                  true,
		httpclient.ErrChallengeRequired:                                   true,
		httpclient.ErrAccountSuspended:                                    true,
		httpclient.ErrRateLimited:                                         false,
		errors.New("dial tcp: connection refused"):                        false,
		fmt.Errorf("wrapped: %w", httpclient.ErrTokenInvalidatedRedirect): true,
	} {
		why, got := sessionOver(err)
		if got != over || (over && why == "") {
			t.Errorf("%v: over = %v (%q), want %v", err, got, why, over)
		}
	}
}
