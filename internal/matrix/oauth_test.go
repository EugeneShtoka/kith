package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// workingKeyring resets go-keyring's mock to a working, empty state. The matrix
// package has other tests that mock it as unavailable (adapter_test.go `noKeyring`),
// and that state is process-wide. OAuth tests touching the keyring must reset it on
// entry; they also drop t.Parallel so the global state can't race.
func workingKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
}

// fakeMAS is a Matrix Authentication Service stand-in — enough of the endpoints to
// drive OAuth, SSO and whoami. Flags toggle the specific failure modes the tests
// exercise; the defaults are a happy path.
type fakeMAS struct {
	// metadataJSON overrides the discovery response (default: real-looking metadata).
	metadataJSON string
	// supportsOAuth, supportsSSO, supportsPassword decide what GetLoginFlows reports
	// and whether /auth_metadata answers at all.
	supportsOAuth, supportsSSO, supportsPassword bool

	// registerReject makes /register return 400.
	registerReject bool
	// callbackError makes the authorize endpoint redirect with ?error=access_denied.
	callbackError string
	// callbackStateOverride replaces the state on the callback (CSRF test).
	callbackStateOverride string
	// tokenReject makes the token endpoint return 400 on the first exchange.
	tokenReject bool
	// refreshReject makes the token endpoint return 401 on grant_type=refresh_token.
	refreshReject bool
	// nextAccessToken is what the token endpoint returns on refresh (bumps so tests
	// can see a refresh happened).
	nextAccessToken atomic.Value // string
	// expiresIn is the lifetime of each access token (default: 1h).
	expiresIn time.Duration

	// For SSO:
	// ssoNoLoginToken makes the SSO redirect come back without ?loginToken=.
	ssoNoLoginToken bool

	// Observed:
	registerCalls, tokenCalls, refreshCalls, whoamiCalls atomic.Int32
}

func newFakeMAS() *fakeMAS {
	f := &fakeMAS{supportsOAuth: true, supportsPassword: true, expiresIn: time.Hour}
	f.nextAccessToken.Store("access-1")
	return f
}

func (f *fakeMAS) metadata(base string) string {
	if f.metadataJSON != "" {
		return f.metadataJSON
	}
	return fmt.Sprintf(`{
		"issuer":%q, "authorization_endpoint":%q, "token_endpoint":%q,
		"registration_endpoint":%q, "revocation_endpoint":%q,
		"response_types_supported":["code"], "response_modes_supported":["query"],
		"grant_types_supported":["authorization_code","refresh_token"],
		"code_challenge_methods_supported":["S256"]
	}`, base, base+"/oauth/authorize", base+"/oauth/token", base+"/oauth/register", base+"/oauth/revoke")
}

// server wraps fakeMAS in an httptest server with every endpoint the two login flows
// touch.
func (f *fakeMAS) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Discovery: 404 when OAuth is turned off.
	mux.HandleFunc("/_matrix/client/v1/auth_metadata", func(w http.ResponseWriter, r *http.Request) {
		if !f.supportsOAuth {
			http.NotFound(w, r)
			return
		}
		base := "http://" + r.Host
		_, _ = fmt.Fprint(w, f.metadata(base))
	})
	// Login flows: lists what the server supports (OAuth servers also return it).
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			var flows []map[string]string
			if f.supportsPassword {
				flows = append(flows, map[string]string{"type": "m.login.password"})
			}
			if f.supportsSSO {
				flows = append(flows, map[string]string{"type": "m.login.sso"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"flows": flows})
			return
		}
		// POST /login — password or token (SSO's second half).
		var body struct {
			Type  string `json:"type"`
			Token string `json:"token"`
			User  struct {
				User string `json:"user"`
			} `json:"identifier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w,
			`{"user_id":"@eugene:example.org","device_id":"DEV1","access_token":"sso-access","home_server":"example.org"}`)
	})
	// OAuth: dynamic client registration.
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		f.registerCalls.Add(1)
		if f.registerReject {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		_, _ = fmt.Fprintf(w, `{"client_id":"client-xyz"}`)
	})
	// OAuth: authorize endpoint — a real server shows a login page. Here it redirects
	// straight to the loopback with the state the caller sent (or the override).
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirect, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect", http.StatusBadRequest)
			return
		}
		rq := redirect.Query()
		state := q.Get("state")
		if f.callbackStateOverride != "" {
			state = f.callbackStateOverride
		}
		if f.callbackError != "" {
			rq.Set("error", f.callbackError)
			rq.Set("error_description", "fake "+f.callbackError)
			rq.Set("state", state)
		} else {
			rq.Set("code", "the-code")
			rq.Set("state", state)
		}
		redirect.RawQuery = rq.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	// OAuth: token endpoint — exchange code or refresh.
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			f.tokenCalls.Add(1)
			if f.tokenReject {
				http.Error(w, "no", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":%d,"refresh_token":"refresh-1"}`,
				f.nextAccessToken.Load().(string), int(f.expiresIn.Seconds()))
		case "refresh_token":
			f.refreshCalls.Add(1)
			if f.refreshReject {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"refresh revoked"}`)
				return
			}
			f.nextAccessToken.Store("access-2")
			_, _ = fmt.Fprintf(w, `{"access_token":"access-2","token_type":"Bearer","expires_in":%d,"refresh_token":"refresh-2"}`,
				int(f.expiresIn.Seconds()))
		default:
			http.Error(w, "unknown grant", http.StatusBadRequest)
		}
	})
	// Whoami: anything with the current token passes.
	mux.HandleFunc("/_matrix/client/v3/account/whoami", func(w http.ResponseWriter, r *http.Request) {
		f.whoamiCalls.Add(1)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errcode":"M_MISSING_TOKEN"}`)
			return
		}
		// A stale token means the client did not refresh in time — reject so a test
		// sees the failure clearly.
		cur, _ := f.nextAccessToken.Load().(string)
		if cur != "" && tok != cur && tok != "access-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"user_id":"@eugene:example.org","device_id":"DEV1"}`)
	})
	// SSO: redirect to the given redirectUrl, optionally without a loginToken.
	mux.HandleFunc("/_matrix/client/v3/login/sso/redirect", func(w http.ResponseWriter, r *http.Request) {
		back, err := url.Parse(r.URL.Query().Get("redirectUrl"))
		if err != nil {
			http.Error(w, "bad redirect", http.StatusBadRequest)
			return
		}
		q := back.Query()
		if !f.ssoNoLoginToken {
			q.Set("loginToken", "login-token-1")
		}
		back.RawQuery = q.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// browserFollow is the test's "browser": take the authorize URL, follow one redirect
// (which goes to the loopback). Called from a goroutine so t.Fatal is out; failures
// go through t.Errorf and the test fails on the real assertion once LoginOAuth times
// out on ctx (every OAuth test wraps t.Context with a timeout).
func browserFollow(t *testing.T, authURL string) {
	t.Helper()
	// Hold the test open until the goroutine returns: a test that fails on its own
	// assertion (SSO without loginToken, callback error, …) returns quickly and the
	// browser hit lands afterwards, where t.Error would panic.
	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	go func() {
		defer close(done)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		ctx := t.Context()
		resp, err := do(ctx, client, authURL)
		if err != nil {
			return // ctx cancellation after the test ends is not a failure
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			return
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return
		}
		back, err := do(ctx, client, loc)
		if err != nil {
			return
		}
		back.Body.Close()
	}()
}

// do runs a GET through client under ctx, so noctx stays happy.
func do(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// browserCtx is a short-lived test context so a flow that never comes back fails
// fast instead of letting go test's 10-minute default run out.
func browserCtx(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(t.Context(), 5*time.Second)
}

// testKeys returns a keyring Store that puts tests on their own service, so the
// client_id cache does not leak between runs.
func testKeys(t *testing.T) session.Store {
	t.Helper()
	return session.Store{
		Service: "kith-test-" + t.Name(),
		Account: "oauth",
		File:    t.TempDir() + "/session.json",
	}
}

// OAuth happy path: discover, register, authorize, callback, exchange, whoami. The
// returned session carries the refresh token and the client_id for later refresh.
func TestOAuthHappyPathReturnsRefreshToken(t *testing.T) {
	workingKeyring(t)
	fake := newFakeMAS()
	srv := fake.server(t)

	flow, err := DiscoverFlow(t.Context(), srv.URL)
	if err != nil || flow != FlowOAuth {
		t.Fatalf("DiscoverFlow = (%v, %v), want (FlowOAuth, nil)", flow, err)
	}

	keys := testKeys(t)
	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	sess, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", keys, show)
	if err != nil {
		t.Fatalf("LoginOAuth: %v", err)
	}
	if sess.AccessToken == "" || sess.RefreshToken == "" || sess.OAuthClientID == "" {
		t.Errorf("session missing fields: %+v", sess)
	}
	if sess.AccessTokenExpiresAt.IsZero() || time.Until(sess.AccessTokenExpiresAt) < time.Minute {
		t.Errorf("expiry %v is not a sensible future time", sess.AccessTokenExpiresAt)
	}
	if sess.UserID != "@eugene:example.org" {
		t.Errorf("UserID = %q, want @eugene:example.org", sess.UserID)
	}
	if fake.registerCalls.Load() != 1 || fake.tokenCalls.Load() != 1 {
		t.Errorf("register=%d token=%d, want 1 each", fake.registerCalls.Load(), fake.tokenCalls.Load())
	}
}

// A callback with the wrong state is a CSRF — the exchange is refused.
func TestOAuthCSRFStateMismatchIsRefused(t *testing.T) {
	workingKeyring(t)
	fake := newFakeMAS()
	fake.callbackStateOverride = "not-the-one-we-sent"
	srv := fake.server(t)

	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	_, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", testKeys(t), show)
	if !errors.Is(err, ErrOAuthStateMismatch) {
		t.Fatalf("err = %v, want ErrOAuthStateMismatch", err)
	}
	if fake.tokenCalls.Load() != 0 {
		t.Errorf("token endpoint hit on a CSRF mismatch")
	}
}

// A callback carrying ?error= is reported as refused with the server's message, so
// `kith login` tells the user why rather than retrying silently.
func TestOAuthCallbackErrorIsReported(t *testing.T) {
	workingKeyring(t)
	fake := newFakeMAS()
	fake.callbackError = "access_denied"
	srv := fake.server(t)

	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	_, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", testKeys(t), show)
	if !errors.Is(err, ErrOAuthRefused) {
		t.Fatalf("err = %v, want ErrOAuthRefused", err)
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("err %q does not name the reason", err)
	}
}

// Discovery falls back to SSO when OAuth is off, and to password when both are off.
// A server that answers neither errors; it does not silently accept password.
func TestDiscoverFlowFallsBackByOrder(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]struct {
		oauth, sso, pwd bool
		want            LoginFlow
	}{
		"oauth":    {oauth: true, pwd: true, want: FlowOAuth},
		"sso":      {sso: true, pwd: true, want: FlowSSO},
		"password": {pwd: true, want: FlowPassword},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeMAS()
			fake.supportsOAuth, fake.supportsSSO, fake.supportsPassword = cfg.oauth, cfg.sso, cfg.pwd
			got, err := DiscoverFlow(t.Context(), fake.server(t).URL)
			if err != nil || got != cfg.want {
				t.Errorf("DiscoverFlow = (%v, %v), want (%v, nil)", got, err, cfg.want)
			}
		})
	}
	t.Run("nothing", func(t *testing.T) {
		t.Parallel()
		fake := newFakeMAS()
		fake.supportsOAuth, fake.supportsSSO, fake.supportsPassword = false, false, false
		if _, err := DiscoverFlow(t.Context(), fake.server(t).URL); err == nil {
			t.Error("DiscoverFlow with no supported flow returned nil, want an error")
		}
	})
}

// SSO happy path: redirect, callback carries loginToken, /login{m.login.token}
// returns a session. No refresh token (SSO uses the standard access-token lifecycle).
func TestSSOHappyPathReturnsAccessToken(t *testing.T) {
	t.Parallel()
	fake := newFakeMAS()
	fake.supportsOAuth, fake.supportsSSO = false, true
	srv := fake.server(t)

	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	sess, err := New(nil).LoginSSO(t.Context(), srv.URL, "@eugene:example.org", show)
	if err != nil {
		t.Fatalf("LoginSSO: %v", err)
	}
	if sess.AccessToken != "sso-access" {
		t.Errorf("AccessToken = %q, want sso-access", sess.AccessToken)
	}
	if sess.RefreshToken != "" || !sess.AccessTokenExpiresAt.IsZero() {
		t.Errorf("SSO session carries OAuth fields it should not: %+v", sess)
	}
}

// SSO with no loginToken on the callback is refused rather than treated as success.
func TestSSOWithoutLoginTokenIsRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeMAS()
	fake.supportsOAuth, fake.supportsSSO, fake.ssoNoLoginToken = false, true, true
	srv := fake.server(t)

	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	if _, err := New(nil).LoginSSO(t.Context(), srv.URL, "@eugene:example.org", show); err == nil {
		t.Fatal("LoginSSO with no loginToken returned nil, want an error")
	}
}

// An expired OAuth access token across a restart: Resume wires the refresh hook, the
// first request triggers a refresh, and the saveNewToken callback writes the fresh
// token into the keyring.
func TestResumeRefreshesExpiredAccessToken(t *testing.T) {
	t.Parallel()
	fake := newFakeMAS()
	srv := fake.server(t)

	saved := domain.Session{
		Homeserver: srv.URL, UserID: "@eugene:example.org", DeviceID: "DEV1",
		AccessToken: "access-1", RefreshToken: "refresh-1", OAuthClientID: "client-xyz",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute), // already dead
	}
	var saved2 domain.Session
	hook := func(_ context.Context, refresh, access string, expiry time.Time) error {
		saved2 = saved
		saved2.AccessToken, saved2.RefreshToken, saved2.AccessTokenExpiresAt = access, refresh, expiry
		return nil
	}
	b := New(nil)
	if err := b.Resume(t.Context(), saved, hook); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if fake.refreshCalls.Load() != 1 {
		t.Errorf("refresh called %d times, want 1", fake.refreshCalls.Load())
	}
	if saved2.AccessToken != "access-2" || saved2.RefreshToken != "refresh-2" {
		t.Errorf("saveNewToken did not run with the refreshed tokens: %+v", saved2)
	}
}

// A refresh the server has revoked becomes ErrSessionRejected, so Start waits for a
// new login rather than looping on a dead session.
func TestResumeOnRevokedRefreshIsSessionRejected(t *testing.T) {
	t.Parallel()
	fake := newFakeMAS()
	fake.refreshReject = true
	srv := fake.server(t)

	saved := domain.Session{
		Homeserver: srv.URL, UserID: "@eugene:example.org", DeviceID: "DEV1",
		AccessToken: "access-1", RefreshToken: "refresh-1", OAuthClientID: "client-xyz",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute),
	}
	err := New(nil).Resume(t.Context(), saved, func(context.Context, string, string, time.Time) error { return nil })
	if !errors.Is(err, api.ErrSessionRejected) {
		t.Fatalf("Resume on revoked refresh = %v, want ErrSessionRejected", err)
	}
}

// A fresh registration is cached in the keyring under the homeserver's URL, so a
// second LoginOAuth to the same homeserver re-uses it.
func TestOAuthClientIDIsCachedPerHomeserver(t *testing.T) {
	workingKeyring(t)
	fake := newFakeMAS()
	srv := fake.server(t)

	keys := testKeys(t)
	show := func(authURL, _ string) error {
		browserFollow(t, authURL)
		return nil
	}
	if _, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", keys, show); err != nil {
		t.Fatalf("first LoginOAuth: %v", err)
	}
	if _, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", keys, show); err != nil {
		t.Fatalf("second LoginOAuth: %v", err)
	}
	if got := fake.registerCalls.Load(); got != 1 {
		t.Errorf("register called %d times for the same homeserver, want 1", got)
	}
	// Clean the keyring entry so a re-run of the suite starts fresh.
	t.Cleanup(func() {
		_ = session.DeleteSecret(keys.Service, oauthClientEntry(srv.URL))
	})
}

// A server that advertises MAS but returns garbage metadata is treated as "not MAS":
// discovery falls through to the next supported flow rather than reporting the OAuth
// failure as a fatal login error. (If no flow is supported at all, the error from
// GetLoginFlows propagates — see TestDiscoverFlowFallsBackByOrder/nothing.)
func TestOAuthMetadataThatDoesNotParseFallsThroughToLoginFlows(t *testing.T) {
	t.Parallel()
	fake := newFakeMAS()
	fake.metadataJSON = `{"issuer":`
	srv := fake.server(t)

	got, err := DiscoverFlow(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("DiscoverFlow with bad metadata = %v, want fallthrough", err)
	}
	if got != FlowPassword {
		t.Errorf("DiscoverFlow with bad metadata = %v, want FlowPassword", got)
	}
}

// Dynamic registration rejected: LoginOAuth fails cleanly without leaving a cached
// client_id behind that a retry would then trust.
func TestOAuthRegistrationRefusedFailsCleanly(t *testing.T) {
	workingKeyring(t)
	fake := newFakeMAS()
	fake.registerReject = true
	srv := fake.server(t)

	keys := testKeys(t)
	show := func(string, string) error { return nil }
	_, err := New(nil).LoginOAuth(t.Context(), srv.URL, "@eugene:example.org", keys, show)
	if err == nil {
		t.Fatal("LoginOAuth with registration refused returned nil, want an error")
	}
	if cached, _ := session.Secret(keys.Service, oauthClientEntry(srv.URL)); cached != "" {
		t.Errorf("cached client_id %q after a refused registration", cached)
	}
}
