package matrix

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// passwordLogin logs the config's user in with password and saves the session. A
// Matrix waiting for one starts on it at once; one already running keeps its
// session, and the new one is used from the next start.
func (m *Adapter) passwordLogin(ctx context.Context, password string) (matrixLoggedIn, error) {
	m.logins.Lock()
	defer m.logins.Unlock()
	sess, err := New(nil).Login(ctx, m.account.Homeserver, m.account.User, password)
	if err != nil {
		return matrixLoggedIn{}, fmt.Errorf("log in as %s: %w", m.account.User, err)
	}
	if err = session.Save(m.account.Crypto.Keys, sess, m.account.AllowTokenFile); err != nil {
		return matrixLoggedIn{}, fmt.Errorf("save session: %w", err)
	}
	out := matrixLoggedIn{UserID: sess.UserID, DeviceID: sess.DeviceID}
	started, err := m.handOver(ctx, sess)
	if err != nil {
		return out, err
	}
	out.Started = started
	m.log.Info("logged in to Matrix", "device", sess.DeviceID, "started", out.Started)
	return out, nil
}

// matrixLoggedIn is a Matrix login's outcome: Started when Matrix started on the
// session at once, false when it was saved for the daemon's next start.
type matrixLoggedIn struct {
	UserID, DeviceID string
	Started          bool
}

// LoginNetwork is Matrix as a login lists it: the config's account.
func (m *Adapter) LoginNetwork() api.LoginNetwork {
	return api.LoginNetwork{
		Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver",
		Accounts: []api.LoginAccount{{Name: m.account.User, Detail: m.account.Homeserver}},
	}
}

// Login logs the config's account in with its password, asked again when refused. A
// session the running daemon cannot start on (its store opened already) starts with
// the next one.
func (m *Adapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account != m.account.User {
		return api.LoginEnd{}, fmt.Errorf("%w: this daemon runs Matrix as %s; another account is a [[profile]] "+
			"(`kith login --profile <name>`)", api.ErrNotOnNetwork, m.account.User)
	}
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "password", Label: "password", Secret: true,
			Help: "The password of " + m.account.User + ". kith logs in as a new session and keeps that session " +
				"in the system keyring, not the password."})
		if err != nil {
			return api.LoginEnd{}, err
		}
		in, err := m.passwordLogin(ctx, got["password"])
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

// Setup sets a Matrix account up in a config that names none: its homeserver
// and user are written, and the daemon restarted to run Matrix, whose encryption
// store opens at start; the login then goes on there.
type Setup struct{}

var matrixIDShape = regexp.MustCompile(`^@[^:\s]+:\S+$`)

// LoginNetwork is Matrix, with no account yet.
func (Setup) LoginNetwork() api.LoginNetwork {
	return api.LoginNetwork{Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver"}
}

// Login asks for the homeserver and the Matrix ID, and has them written.
func (Setup) Login(ctx context.Context, _ string, talk api.LoginTalk) (api.LoginEnd, error) {
	var homeserver, note string
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "homeserver", Label: "homeserver", Value: "https://matrix.org",
			Help: "Your homeserver's address: https://matrix.org for an account there, or your own server's — what " +
				"follows the colon in your Matrix ID (@you:matrix.org)."})
		if err != nil {
			return api.LoginEnd{}, err
		}
		homeserver, note = strings.TrimSpace(got["homeserver"]), ""
		if !strings.Contains(homeserver, "://") {
			homeserver = "https://" + homeserver
		}
		if u, err := url.Parse(homeserver); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			note = "write the homeserver's address, as https://matrix.org"
			continue
		}
		homeserver = strings.TrimSuffix(homeserver, "/")
		break
	}
	var user string
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "user", Label: "Matrix ID",
			Help: "Your Matrix ID: @you:matrix.org. The name alone is taken as on this homeserver."})
		if err != nil {
			return api.LoginEnd{}, err
		}
		user = strings.TrimSpace(got["user"])
		if !strings.Contains(user, ":") {
			host := ""
			if u, err := url.Parse(homeserver); err == nil {
				host = u.Hostname()
			}
			user = "@" + strings.TrimPrefix(user, "@") + ":" + host
		}
		if matrixIDShape.MatchString(user) {
			break
		}
		note = "write your Matrix ID, as @you:matrix.org"
	}
	if err := talk.Configure(ctx, api.LoginRecord{Values: map[string]string{"homeserver": homeserver, "user": user}}); err != nil {
		return api.LoginEnd{}, err
	}
	talk.Named(user)
	return api.LoginEnd{Done: "set Matrix up as " + user, Restart: true}, nil
}

// handOver gives a waiting adapter the session, reporting whether it took it. One
// trying another session is waited for: if that one is rejected, this one is wanted.
func (m *Adapter) handOver(ctx context.Context, sess domain.Session) (bool, error) {
	for {
		m.mu.Lock()
		switch m.phase {
		case awaitingLogin:
			m.pending = &sess
			m.enterResuming()
			m.mu.Unlock()
			select {
			case m.wake <- struct{}{}:
			default: // a signal is already there; Start takes the pending session
			}
			return true, nil
		case running:
			m.mu.Unlock()
			return false, nil
		case resuming:
			settled := m.settled
			m.mu.Unlock()
			select {
			case <-settled:
			case <-ctx.Done():
				return false, fmt.Errorf("saved, but Matrix is still connecting on another session: %w", ctx.Err())
			}
		}
	}
}
