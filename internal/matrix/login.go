package matrix

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// errNoAccount is a login naming no configured account.
var errNoAccount = fmt.Errorf("%w: no such [[matrix.account]]", api.ErrNotOnNetwork)

// passwordLogin logs the account in and saves the session. A waiting runner starts on
// it at once; a running one keeps its session until the next start.
func (r *runner) passwordLogin(ctx context.Context, password string) (matrixLoggedIn, error) {
	r.logins.Lock()
	defer r.logins.Unlock()
	acc := r.account()
	sess, err := New(nil).Login(ctx, acc.Homeserver, acc.User, password)
	if err != nil {
		return matrixLoggedIn{}, fmt.Errorf("log in as %s: %w", acc.User, err)
	}
	if err = session.Save(acc.Crypto.Keys, sess, acc.AllowTokenFile); err != nil {
		return matrixLoggedIn{}, fmt.Errorf("save session: %w", err)
	}
	out := matrixLoggedIn{UserID: sess.UserID, DeviceID: sess.DeviceID}
	started, err := r.handOver(ctx, sess)
	if err != nil {
		return out, err
	}
	out.Started = started
	r.log.Info("logged in to Matrix", "device", sess.DeviceID, "started", out.Started)
	return out, nil
}

// matrixLoggedIn is a login's outcome; Started is false when the session waits for
// the daemon's next start.
type matrixLoggedIn struct {
	UserID, DeviceID string
	Started          bool
}

// LoginNetwork is Matrix as a login lists it, with each configured account.
func (m *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver"}
	for _, r := range m.runnersNow() {
		acc := r.account()
		n.Accounts = append(n.Accounts, api.LoginAccount{Name: acc.Name, Detail: acc.User})
	}
	return n
}

// Login logs the account called name in ("" sets up a new one, writing it into the
// config) through whichever flow the homeserver offers: OAuth 2.0 (MAS) first, then
// m.login.sso, then m.login.password.
func (m *Adapter) Login(ctx context.Context, name string, talk api.LoginTalk) (api.LoginEnd, error) {
	if name == "" {
		var err error
		if name, err = setUp(ctx, talk, m.LoginNetwork().Accounts); err != nil {
			return api.LoginEnd{}, err
		}
	}
	r, err := m.runnerNamed(name)
	if err != nil {
		return api.LoginEnd{}, err
	}
	acc := r.account()
	flow, err := DiscoverFlow(ctx, acc.Homeserver)
	if err != nil {
		return api.LoginEnd{}, fmt.Errorf("matrix: discover login flow: %w", err)
	}
	switch flow {
	case FlowOAuth:
		return r.browserLogin(ctx, talk, FlowOAuth)
	case FlowSSO:
		return r.browserLogin(ctx, talk, FlowSSO)
	default:
		return r.passwordLoop(ctx, talk)
	}
}

// runnerNamed is the runner of the account the config calls name.
func (m *Adapter) runnerNamed(name string) (*runner, error) {
	for _, r := range m.runnersNow() {
		if r.account().Name == name {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%w named %q", errNoAccount, name)
}

// passwordLoop is the pre-OAuth flow kept for homeservers that still only answer
// m.login.password: ask for the password, retry with a note on refusal.
func (r *runner) passwordLoop(ctx context.Context, talk api.LoginTalk) (api.LoginEnd, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "password", Label: "password", Secret: true,
			Help: "The password of " + r.account().User + ". kith logs in as a new session and keeps that session " +
				"in the system keyring, not the password."})
		if err != nil {
			return api.LoginEnd{}, err
		}
		in, err := r.passwordLogin(ctx, got["password"])
		if err != nil {
			if ctx.Err() != nil {
				return api.LoginEnd{}, err
			}
			note = err.Error()
			continue
		}
		return api.LoginEnd{Done: "logged in to Matrix as " + in.UserID, Restart: !in.Started}, nil
	}
}

// browserLogin runs the OAuth or SSO flow, same user-facing shape: start a loopback
// server, show the authorize URL via Show, wait for the browser's callback here, then
// hand the session over to a waiting start.
func (r *runner) browserLogin(ctx context.Context, talk api.LoginTalk, flow LoginFlow) (api.LoginEnd, error) {
	r.logins.Lock()
	defer r.logins.Unlock()
	acc := r.account()
	show := func(authURL, note string) error { return talk.Show(ctx, authURL, note) }
	var sess domain.Session
	var err error
	switch flow {
	case FlowOAuth:
		sess, err = New(nil).LoginOAuth(ctx, acc.Homeserver, acc.User, acc.Crypto.Keys, show)
	case FlowSSO:
		sess, err = New(nil).LoginSSO(ctx, acc.Homeserver, acc.User, show)
	default:
		return api.LoginEnd{}, fmt.Errorf("matrix: unexpected browser flow %d", flow)
	}
	if err != nil {
		return api.LoginEnd{}, fmt.Errorf("log in as %s: %w", acc.User, err)
	}
	if err = session.Save(acc.Crypto.Keys, sess, acc.AllowTokenFile); err != nil {
		return api.LoginEnd{}, fmt.Errorf("save session: %w", err)
	}
	started, err := r.handOver(ctx, sess)
	if err != nil {
		return api.LoginEnd{Done: "logged in to Matrix as " + sess.UserID, Restart: !started}, err
	}
	r.log.Info("logged in to Matrix", "device", sess.DeviceID, "started", started, "flow", flow)
	return api.LoginEnd{Done: "logged in to Matrix as " + sess.UserID, Restart: !started}, nil
}

var matrixIDShape = regexp.MustCompile(`^@[^:\s]+:\S+$`)

// setUp asks for a homeserver, a Matrix ID none of accounts has and what to call the
// account, writes it into the config as a [[matrix.account]] and returns its name;
// Configure returns once the daemon has re-read it.
func setUp(ctx context.Context, talk api.LoginTalk, accounts []api.LoginAccount) (string, error) {
	homeserver, err := askHomeserver(ctx, talk)
	if err != nil {
		return "", err
	}
	user, err := askUser(ctx, talk, homeserver, accounts)
	if err != nil {
		return "", err
	}
	taken := make([]string, 0, len(accounts))
	for _, a := range accounts {
		taken = append(taken, a.Name)
	}
	name, err := api.AskName(ctx, talk, suggestedName(user, taken), taken,
		"What kith calls this account: its rooms are the space “Matrix <name>” in the rail, and "+
			"`kith login matrix <name>` logs it in again. Suggested from the Matrix ID; enter keeps it.")
	if err != nil {
		return "", err
	}
	record := api.LoginRecord{Table: "matrix.account", Values: map[string]string{"name": name, "homeserver": homeserver, "user": user}}
	if err := talk.Configure(ctx, record); err != nil {
		return "", err
	}
	talk.Named(name)
	return name, nil
}

// askHomeserver asks for a homeserver's address until it is one.
func askHomeserver(ctx context.Context, talk api.LoginTalk) (string, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "homeserver", Label: "homeserver", Value: "https://matrix.org",
			Help: "Your homeserver's address: https://matrix.org for an account there, or your own server's — what " +
				"follows the colon in your Matrix ID (@you:matrix.org)."})
		if err != nil {
			return "", err
		}
		homeserver := strings.TrimSpace(got["homeserver"])
		if !strings.Contains(homeserver, "://") {
			homeserver = "https://" + homeserver
		}
		if u, err := url.Parse(homeserver); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			note = "write the homeserver's address, as https://matrix.org"
			continue
		}
		return strings.TrimSuffix(homeserver, "/"), nil
	}
}

// askUser asks for a Matrix ID on homeserver none of accounts has.
func askUser(ctx context.Context, talk api.LoginTalk, homeserver string, accounts []api.LoginAccount) (string, error) {
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "user", Label: "Matrix ID",
			Help: "Your Matrix ID: @you:matrix.org. The name alone is taken as on this homeserver."})
		if err != nil {
			return "", err
		}
		user := strings.TrimSpace(got["user"])
		if !strings.Contains(user, ":") {
			host := ""
			if u, err := url.Parse(homeserver); err == nil {
				host = u.Hostname()
			}
			user = "@" + strings.TrimPrefix(user, "@") + ":" + host
		}
		switch i := slices.IndexFunc(accounts, func(a api.LoginAccount) bool { return a.Detail == user }); {
		case i >= 0:
			note = "that is the account " + accounts[i].Name + " already; log it in from the list"
		case !matrixIDShape.MatchString(user):
			note = "write your Matrix ID, as @you:matrix.org"
		default:
			return user, nil
		}
	}
}

// suggestedName is what to call user's account: its localpart, else with a number
// when that is taken.
func suggestedName(user string, taken []string) string {
	local, _, _ := strings.Cut(strings.TrimPrefix(user, "@"), ":")
	name := local
	for n := 2; slices.Contains(taken, name); n++ {
		name = fmt.Sprintf("%s%d", local, n)
	}
	return name
}
