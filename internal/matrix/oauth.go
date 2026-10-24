// Login flow discovery and browser-redirect sign-in. Three paths, in order of what
// the homeserver says it supports:
//
//   - OAuth 2.0 / OIDC via Matrix Authentication Service (MSC3861 / MSC2965). Dynamic
//     client registration (RFC 7591) + authorization-code flow with PKCE + S256; the
//     refresh token lives in the keyring beside the access token and is used
//     automatically on expiry.
//   - m.login.sso: the homeserver redirects to its IdP and back to kith with a
//     loginToken, which /_matrix/client/v3/login exchanges for an access token.
//   - m.login.password: today's flow, run by login.go.
//
// Both redirect flows start a loopback HTTP server on 127.0.0.1:0 for the callback.
// The authorization URL is shown via LoginTalk.Show; the user opens it, signs in and
// comes back here.

package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/oauth"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// LoginFlow is one way to sign in; the server picks what it supports and the client
// takes the first it can run.
type LoginFlow int

const (
	// FlowPassword is m.login.password: the fallback, driven from login.go.
	FlowPassword LoginFlow = iota
	// FlowOAuth is OIDC via MAS: a browser signs in and returns here with a code.
	FlowOAuth
	// FlowSSO is m.login.sso: a browser signs in and returns here with a loginToken.
	FlowSSO
)

// DiscoverFlow asks homeserver which login paths it supports and returns the best
// one kith can take. OAuth beats SSO beats password; an unreachable discovery call
// errors, so a login never silently falls back to password after a network blip.
func DiscoverFlow(ctx context.Context, homeserver string) (LoginFlow, error) {
	client, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return FlowPassword, fmt.Errorf("new client: %w", err)
	}
	if _, oerr := client.OAuthGetServerMetadata(ctx); oerr == nil {
		return FlowOAuth, nil
	} else if ctx.Err() != nil {
		return FlowPassword, fmt.Errorf("discover oauth metadata: %w", ctx.Err())
	}
	flows, err := client.GetLoginFlows(ctx)
	if err != nil {
		return FlowPassword, fmt.Errorf("get login flows: %w", err)
	}
	switch {
	case flows.HasFlow(mautrix.AuthTypeSSO):
		return FlowSSO, nil
	case flows.HasFlow(mautrix.AuthTypePassword):
		return FlowPassword, nil
	default:
		return FlowPassword, errors.New("homeserver supports neither OAuth, SSO nor password login")
	}
}

const (
	// oauthClientName is what MAS admins see for kith's dynamic client registration.
	oauthClientName = "kith"
	// oauthClientURI is what RFC 7591 wants as a stable identifier for the native app.
	oauthClientURI = "https://github.com/EugeneShtoka/kith"
	// oauthCallbackPath is the loopback redirect endpoint; the port is picked at
	// bind time so two kith logins in the same minute don't clash.
	oauthCallbackPath = "/callback"
)

// oauthScopes grants kith the client API and binds the session to a device so the
// E2EE keys never change identity. The device ID is picked by kith at login.
func oauthScopes(deviceID id.DeviceID) oauth.ScopeList {
	return oauth.ScopeList{oauth.ScopeOpenID, oauth.ScopeClientAPI, oauth.ScopeDevice(deviceID)}
}

// oauthClientEntry is the keyring entry under which a homeserver's registered
// client_id is cached: register once per homeserver, reuse forever (clients are the
// app, not the session).
func oauthClientEntry(homeserver string) string { return "oauth-client|" + homeserver }

// ensureOAuthClient returns the client_id to use for homeserver: a cached one, or a
// freshly registered one it caches first. Fresh registration is bound to the
// loopback redirect URI, as the exchange will carry it back.
func ensureOAuthClient(
	ctx context.Context, cli *mautrix.Client, keys session.Store, homeserver, redirectURI string,
) (string, error) {
	cached, _ := session.Secret(keys.Service, oauthClientEntry(homeserver))
	if cached != "" {
		return cached, nil
	}
	resp, err := cli.OAuthRegisterClient(ctx, &oauth.ClientMetadata{
		ApplicationType:         oauth.ApplicationTypeNative,
		ClientName:              oauthClientName,
		ClientURI:               oauthClientURI,
		GrantTypes:              []oauth.GrantType{oauth.GrantTypeAuthorizationCode, oauth.GrantTypeRefreshToken},
		RedirectURIs:            []string{redirectURI},
		ResponseTypes:           []oauth.ResponseType{oauth.ResponseTypeCode},
		TokenEndpointAuthMethod: oauth.AuthMethodNone,
	})
	if err != nil {
		return "", fmt.Errorf("register oauth client: %w", err)
	}
	if resp.ClientID == "" {
		return "", errors.New("register oauth client: homeserver returned no client_id")
	}
	if cerr := session.StoreSecret(keys.Service, oauthClientEntry(homeserver), resp.ClientID); cerr != nil {
		return "", fmt.Errorf("cache oauth client_id: %w", cerr)
	}
	return resp.ClientID, nil
}

// callbackPage is served to the browser once the callback fires. It never names the
// details (code, token, error) so copying the URL does not disclose anything.
const callbackPage = `<!doctype html><html><head><meta charset="utf-8"><title>kith</title>` +
	`<style>body{font-family:system-ui,sans-serif;max-width:28em;margin:4em auto;color:#333}` +
	`h1{font-size:1.4em;margin-bottom:.2em}p{line-height:1.4}</style></head>` +
	`<body><h1>kith</h1><p>%s</p></body></html>`

// callback is one browser round-trip's result: either a code+state, a loginToken, or
// an OAuth error reported in the query string.
type callback struct {
	code    string
	state   string
	loginTk string
	errMsg  string
	errDesc string
}

// listenLoopback binds a loopback TCP port and returns the full redirect URL and the
// listener to Serve. The port is 0 so the OS picks a free one.
func listenLoopback() (string, *net.TCPListener, error) {
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return "", nil, fmt.Errorf("open loopback port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, oauthCallbackPath), ln, nil
}

// awaitCallback runs an http.Server on ln and returns the callback the browser comes
// back with. The server stops at the first callback request, and shuts down when ctx
// ends. The caller takes ownership of ln (passed to Serve), so there is no leak on
// any return path.
func awaitCallback(ctx context.Context, ln *net.TCPListener) (callback, error) {
	ch := make(chan callback, 1)
	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc(oauthCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cb := callback{
			code: q.Get("code"), state: q.Get("state"), loginTk: q.Get("loginToken"),
			errMsg: q.Get("error"), errDesc: q.Get("error_description"),
		}
		msg := "You can close this tab now."
		if cb.errMsg != "" {
			msg = fmt.Sprintf("The homeserver refused: %s. You can close this tab now.", cb.errMsg)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprintf(w, callbackPage, msg)
		select {
		case ch <- cb:
		case <-done:
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer func() {
		close(done)
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		<-serveErr
	}()
	select {
	case cb := <-ch:
		return cb, nil
	case <-ctx.Done():
		return callback{}, fmt.Errorf("waiting for the browser: %w", ctx.Err())
	case err := <-serveErr:
		return callback{}, fmt.Errorf("loopback server stopped: %w", err)
	}
}

// LoginOAuth signs in through a MAS homeserver's OAuth flow. show prints the URL to
// the user (via LoginTalk.Show); the browser signs in and comes back to the loopback
// callback, where the code is exchanged for an access + refresh token.
func (b *InProc) LoginOAuth(
	ctx context.Context, homeserver, userHint string,
	keys session.Store, show func(authURL, note string) error,
) (domain.Session, error) {
	client, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: new client: %w", err)
	}
	b.attachLogger(client)
	if _, err = client.OAuthGetServerMetadata(ctx); err != nil {
		return domain.Session{}, fmt.Errorf("matrix: oauth metadata: %w", err)
	}
	cb, authResp, redirectURI, clientID, err := runOAuthBrowser(ctx, client, keys, homeserver, userHint, show)
	if err != nil {
		return domain.Session{}, err
	}
	tokenStart := time.Now()
	tokens, err := client.OAuthExchangeToken(ctx, oauth.ExchangeTokenParams{
		CodeVerifier: authResp.CodeVerifier, RedirectURI: redirectURI,
		Code: cb.code, ClientID: clientID, StoreCredentials: true,
	})
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: exchange code: %w", err)
	}
	who, err := client.Whoami(ctx)
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: whoami: %w", err)
	}
	client.DeviceID = who.DeviceID
	b.client = client
	return domain.Session{
		Homeserver: homeserver, UserID: b.ids.user(who.UserID), DeviceID: string(who.DeviceID),
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken, OAuthClientID: clientID,
		AccessTokenExpiresAt: tokenStart.Add(tokens.ExpiresIn.Duration),
	}, nil
}

// runOAuthBrowser handles the browser half of the OAuth flow: register (or reuse) the
// client, bind the loopback port, build and show the authorize URL, wait for the
// callback, and verify the state. The code (ready to exchange) and the inputs the
// exchange needs come back together so the caller does not re-plumb them.
func runOAuthBrowser(
	ctx context.Context, client *mautrix.Client, keys session.Store,
	homeserver, userHint string, show func(authURL, note string) error,
) (callback, *oauth.AuthorizationCodeResponse, string, string, error) {
	redirectURI, ln, err := listenLoopback()
	if err != nil {
		return callback{}, nil, "", "", fmt.Errorf("matrix: %w", err)
	}
	served := false
	defer func() {
		if !served {
			_ = ln.Close()
		}
	}()
	clientID, err := ensureOAuthClient(ctx, client, keys, homeserver, redirectURI)
	if err != nil {
		return callback{}, nil, "", "", fmt.Errorf("matrix: %w", err)
	}
	deviceID := id.DeviceID(newMatrixDeviceID())
	authResp, err := client.OAuthGetAuthorizationURL(ctx, oauth.GetAuthorizationURLParams{
		RedirectURI: redirectURI, Scopes: oauthScopes(deviceID),
		UserIDHint: configuredUser(userHint), ClientID: clientID,
	})
	if err != nil {
		return callback{}, nil, "", "", fmt.Errorf("matrix: build authorize url: %w", err)
	}
	if err = show(authResp.URL,
		"open this URL in a browser to sign in; this terminal continues when you come back"); err != nil {
		return callback{}, nil, "", "", fmt.Errorf("matrix: show authorize url: %w", err)
	}
	served = true
	cb, err := awaitCallback(ctx, ln)
	if err != nil {
		return callback{}, nil, "", "", fmt.Errorf("matrix: oauth callback: %w", err)
	}
	switch {
	case cb.errMsg != "":
		return callback{}, nil, "", "", fmt.Errorf("%w: %s %s", ErrOAuthRefused, cb.errMsg, cb.errDesc)
	case cb.code == "":
		return callback{}, nil, "", "", errors.New("matrix: oauth callback has no code")
	case cb.state != authResp.State:
		return callback{}, nil, "", "", ErrOAuthStateMismatch
	}
	return cb, authResp, redirectURI, clientID, nil
}

// LoginSSO signs in through m.login.sso: the homeserver redirects to its IdP and back
// here with a loginToken (/_matrix/client/v3/login/sso/redirect). The token is then
// exchanged via /login with AuthTypeToken for a standard access token.
func (b *InProc) LoginSSO(
	ctx context.Context, homeserver, user string,
	show func(ssoURL, note string) error,
) (domain.Session, error) {
	client, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: new client: %w", err)
	}
	b.attachLogger(client)
	redirectURI, ln, err := listenLoopback()
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: %w", err)
	}
	served := false
	defer func() {
		if !served {
			_ = ln.Close()
		}
	}()
	q := url.Values{"redirectUrl": []string{redirectURI}}
	ssoURL := strings.TrimRight(homeserver, "/") + "/_matrix/client/v3/login/sso/redirect?" + q.Encode()
	if err = show(ssoURL,
		"open this URL in a browser to sign in; this terminal continues when you come back"); err != nil {
		return domain.Session{}, fmt.Errorf("matrix: show sso url: %w", err)
	}
	served = true
	cb, err := awaitCallback(ctx, ln)
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: sso callback: %w", err)
	}
	switch {
	case cb.errMsg != "":
		return domain.Session{}, fmt.Errorf("%w: %s %s", ErrOAuthRefused, cb.errMsg, cb.errDesc)
	case cb.loginTk == "":
		return domain.Session{}, errors.New("matrix: sso callback has no loginToken")
	}
	resp, err := client.Login(ctx, &mautrix.ReqLogin{
		Type:             mautrix.AuthTypeToken,
		Token:            cb.loginTk,
		Identifier:       mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		StoreCredentials: true,
	})
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: exchange login token: %w", err)
	}
	b.client = client
	return domain.Session{
		Homeserver: homeserver, UserID: b.ids.user(resp.UserID), DeviceID: string(resp.DeviceID),
		AccessToken: resp.AccessToken,
	}, nil
}

// newMatrixDeviceID picks a short, printable device id so the server has one to bind
// the OAuth scope to. 16 hex chars matches what Synapse itself picks.
func newMatrixDeviceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// ErrOAuthStateMismatch is a CSRF rejection: the state in the callback is not the
// one kith sent, so the code is not exchanged.
var ErrOAuthStateMismatch = errors.New("matrix: oauth callback state mismatch")

// ErrOAuthRefused is a browser callback carrying ?error= — the homeserver turned the
// authorization request down (access_denied, login_required, …).
var ErrOAuthRefused = errors.New("matrix: homeserver refused")
