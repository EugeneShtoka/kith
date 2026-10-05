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
	acc := m.accountNow()
	sess, err := New(nil).Login(ctx, acc.Homeserver, acc.User, password)
	if err != nil {
		return matrixLoggedIn{}, fmt.Errorf("log in as %s: %w", acc.User, err)
	}
	if err = session.Save(acc.Crypto.Keys, sess, acc.AllowTokenFile); err != nil {
		return matrixLoggedIn{}, fmt.Errorf("save session: %w", err)
	}
	out := matrixLoggedIn{UserID: sess.UserID, DeviceID: sess.DeviceID}
	started, err := m.handOver(ctx, sess)
	if err != nil {
		return out, err
	}
	out.Started = started
	m.mu.Lock()
	log := m.userLog
	m.mu.Unlock()
	log.Info("logged in to Matrix", "device", sess.DeviceID, "started", out.Started)
	return out, nil
}

// matrixLoggedIn is a Matrix login's outcome: Started when Matrix started on the
// session at once, false when it was saved for the daemon's next start.
type matrixLoggedIn struct {
	UserID, DeviceID string
	Started          bool
}

// LoginNetwork is Matrix as a login lists it: the config's account, once it has one.
func (m *Adapter) LoginNetwork() api.LoginNetwork {
	n := api.LoginNetwork{Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver"}
	if acc := m.accountNow(); acc.User != "" {
		n.Accounts = []api.LoginAccount{{Name: acc.User, Detail: acc.Homeserver}}
	}
	return n
}

// Login logs the config's account in with its password, asked again when refused,
// setting it up first when the config names none: its homeserver and Matrix ID are
// written into the config, which hands the account over when re-read. A session the
// running Matrix cannot start on (its store opened already) starts with the next one.
func (m *Adapter) Login(ctx context.Context, name string, talk api.LoginTalk) (api.LoginEnd, error) {
	acc := m.accountNow()
	switch {
	case acc.User == "":
		user, err := setUp(ctx, talk)
		if err != nil {
			return api.LoginEnd{}, err
		}
		if acc = m.accountNow(); acc.User != user {
			return api.LoginEnd{}, fmt.Errorf("matrix: the config did not take %s; restart kithd and log in again", user)
		}
		talk.Named(user)
	case name != "" && name != acc.User:
		return api.LoginEnd{}, fmt.Errorf("%w: this daemon runs Matrix as %s; another account is a [[profile]] "+
			"(`kith login --profile <name>`)", api.ErrNotOnNetwork, acc.User)
	}
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "password", Label: "password", Secret: true,
			Help: "The password of " + acc.User + ". kith logs in as a new session and keeps that session " +
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

var matrixIDShape = regexp.MustCompile(`^@[^:\s]+:\S+$`)

// setUp asks for a homeserver and a Matrix ID, has them written into the config, and
// is the ID; Configure returns once the daemon has re-read it.
func setUp(ctx context.Context, talk api.LoginTalk) (string, error) {
	var homeserver, note string
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "homeserver", Label: "homeserver", Value: "https://matrix.org",
			Help: "Your homeserver's address: https://matrix.org for an account there, or your own server's — what " +
				"follows the colon in your Matrix ID (@you:matrix.org)."})
		if err != nil {
			return "", err
		}
		homeserver = strings.TrimSpace(got["homeserver"])
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
	note = ""
	var user string
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "user", Label: "Matrix ID",
			Help: "Your Matrix ID: @you:matrix.org. The name alone is taken as on this homeserver."})
		if err != nil {
			return "", err
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
		return "", err
	}
	return user, nil
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
