package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/session"
)

// matrixAccount is the config's Matrix account and where its secrets are kept.
type matrixAccount struct {
	homeserver, user string
	allowTokenFile   bool
	crypto           cryptoPlace
}

// cryptoPlace is where the Matrix encryption store and its key are kept.
type cryptoPlace struct {
	path string
	keys session.Store
}

// sessionPhase is where the Matrix adapter is with its session.
type sessionPhase int

const (
	// awaitingLogin has no usable session: a login hands one over.
	awaitingLogin sessionPhase = iota
	// resuming is trying a session; a rejected one goes back to awaitingLogin.
	resuming
	// running has opened the crypto store on a session: a login now is saved for the
	// next start, as a second session cannot share the store.
	running
)

// matrixAdapter is the Matrix adapter as the router starts it. It may start with no
// session, or one the homeserver rejects: Start then waits for LoginMatrix, and the
// router treats Matrix as off until LoggedIn. What is left of connecting once the
// homeserver answers belongs to its Start, and holds up no other network.
type matrixAdapter struct {
	*matrix.InProc
	log     *slog.Logger
	account matrixAccount

	// onStatus hears each phase change (the daemon's per-network status); onLoggedIn
	// hears the session taking, so what was skipped while logged out is caught up.
	// Set before Start.
	onStatus   func(domain.AccountStatus)
	onLoggedIn func()

	// loggedIn turns true once the crypto store is open on a session, before
	// anything reaches the adapter through the router.
	loggedIn atomic.Bool

	saved domain.Session // the session to try first; zero when there is none

	// logins runs one login at a time: the session saved last is the newest login's.
	logins sync.Mutex

	mu    sync.Mutex
	phase sessionPhase
	// pending is the session a login handed over, until Start takes it; wake says
	// one was (it holds at most one signal).
	pending *domain.Session
	wake    chan struct{}
	// settled is closed when a resuming phase ends; a login waits on it to learn
	// whether its session is wanted.
	settled chan struct{}
}

// newMatrixAdapter is the adapter for account over cache, starting from saved (zero
// when there is no saved session).
func newMatrixAdapter(cache *db.Cache, log *slog.Logger, account matrixAccount, saved domain.Session) *matrixAdapter {
	m := &matrixAdapter{
		InProc: matrix.New(cache), log: log, account: account,
		onStatus: func(domain.AccountStatus) {}, onLoggedIn: func() {},
		saved: saved, wake: make(chan struct{}, 1),
	}
	m.phase = awaitingLogin
	if saved.AccessToken != "" {
		m.enterResuming()
	}
	m.UseLogger(log)
	return m
}

// Network is Matrix.
func (m *matrixAdapter) Network() domain.Protocol { return domain.ProtocolMatrix }

// OnStatus sets who hears the account's phase change. Set before Start.
func (m *matrixAdapter) OnStatus(changed func(domain.AccountStatus)) { m.onStatus = changed }

// status is the account in phase, saying detail.
func (m *matrixAdapter) status(phase daemon.Phase, detail string) domain.AccountStatus {
	return domain.AccountStatus{Network: domain.ProtocolMatrix, Account: m.account.user, Phase: phase, Detail: detail}
}

// report tells the listener the account's phase changed.
func (m *matrixAdapter) report(phase daemon.Phase, detail string) {
	m.onStatus(m.status(phase, detail))
}

// UseConfig takes [display.deleted] keep and the identities, at start and each time
// the config is re-read. The account itself is the daemon's to change (a restart).
func (m *matrixAdapter) UseConfig(ctx context.Context, cfg config.Config) {
	m.KeepDeleted(cfg.Display.Deleted.Keep())
	m.UseIdentities(ctx, identityGroups(cfg))
}

// SavedSessions is the account when a session was saved: Start resumes it.
func (m *matrixAdapter) SavedSessions(context.Context) ([]string, error) {
	if m.saved.AccessToken == "" {
		return nil, nil
	}
	return []string{m.account.user}, nil
}

// LoggedIn reports whether Matrix has a session the router may use (route.Session).
func (m *matrixAdapter) LoggedIn() bool { return m.loggedIn.Load() }

// hint is what to do to log Matrix in.
const loginHint = "run `kith login`"

// Start tries the saved session, else waits for a login; then finishes connecting
// and runs the sync loop. A rejected session waits for a login again. It blocks.
func (m *matrixAdapter) Start(ctx context.Context) error {
	next := m.saved
	for {
		if next.AccessToken == "" {
			m.report(daemon.PhaseLoggedOut, "no saved session; "+loginHint)
			var ok bool
			if next, ok = m.takeLogin(ctx); !ok {
				return nil // shut down while logged out
			}
		}
		m.report(daemon.PhaseConnecting, "resuming the session")
		prepare, err := resumeSession(ctx, m.log, m.InProc, m.account.crypto, next)
		if errors.Is(err, api.ErrSessionRejected) {
			m.log.Warn("the saved Matrix session was rejected; waiting for `kith login`", "err", err)
			m.report(daemon.PhaseLoggedOut, "the homeserver rejected the session; "+loginHint)
			m.settle(awaitingLogin)
			next = domain.Session{}
			continue
		}
		if err != nil {
			m.report(daemon.PhaseFailed, err.Error())
			// The store may be open: no session can be tried in this process again.
			m.settle(running)
			return err
		}
		m.loggedIn.Store(true)
		m.settle(running)
		m.onLoggedIn()
		return m.run(ctx, prepare)
	}
}

// takeLogin waits for LoginMatrix to hand a session over (one may already have).
func (m *matrixAdapter) takeLogin(ctx context.Context) (domain.Session, bool) {
	for {
		m.mu.Lock()
		if s := m.pending; s != nil {
			m.pending = nil
			m.mu.Unlock()
			return *s, true
		}
		m.mu.Unlock()
		select {
		case <-m.wake:
		case <-ctx.Done():
			return domain.Session{}, false
		}
	}
}

// enterResuming starts a resuming phase. Caller holds mu, or nothing runs yet.
func (m *matrixAdapter) enterResuming() {
	m.phase = resuming
	m.settled = make(chan struct{})
}

// settle ends a resuming phase in p, waking the logins waiting to learn the outcome.
func (m *matrixAdapter) settle(p sessionPhase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase == resuming {
		close(m.settled)
	}
	m.phase = p
}

// run finishes connecting (prepare, nil when the homeserver answered), then syncs.
func (m *matrixAdapter) run(ctx context.Context, prepare func(context.Context) error) error {
	if prepare != nil {
		m.report(daemon.PhaseConnecting, "the homeserver is unreachable; serving cached history")
		if err := daemon.SyncFault(ctx, prepare(ctx)); err != nil {
			m.report(daemon.PhaseFailed, err.Error())
			return fmt.Errorf("connect: %w", err)
		}
	}
	if err := m.InProc.Start(ctx); err != nil {
		if fault := daemon.SyncFault(ctx, err); fault != nil {
			m.report(daemon.PhaseFailed, fault.Error())
		}
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// LoginMatrix logs the config's user in with password and saves the session
// (api.MatrixLogin). A Matrix waiting for one starts on it at once; one already
// running keeps its session, and the new one is used from the next start.
func (m *matrixAdapter) LoginMatrix(ctx context.Context, password string) (matrixLoggedIn, error) {
	m.logins.Lock()
	defer m.logins.Unlock()
	sess, err := matrix.New(nil).Login(ctx, m.account.homeserver, m.account.user, password)
	if err != nil {
		return matrixLoggedIn{}, fmt.Errorf("log in as %s: %w", m.account.user, err)
	}
	if err = session.Save(m.account.crypto.keys, sess, m.account.allowTokenFile); err != nil {
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
func (m *matrixAdapter) LoginNetwork() api.LoginNetwork {
	return api.LoginNetwork{
		Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver",
		Accounts: []api.LoginAccount{{Name: m.account.user, Detail: m.account.homeserver}},
	}
}

// Login logs the config's account in with its password, asked again when refused. A
// session the running daemon cannot start on (its store opened already) starts with
// the next one.
func (m *matrixAdapter) Login(ctx context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account != m.account.user {
		return api.LoginEnd{}, fmt.Errorf("%w: this daemon runs Matrix as %s; another account is a [[profile]] "+
			"(`kith login --profile <name>`)", api.ErrNotOnNetwork, m.account.user)
	}
	note := ""
	for {
		got, err := talk.Ask(ctx, note, api.LoginField{Key: "password", Label: "password", Secret: true,
			Help: "The password of " + m.account.user + ". kith logs in as a new session and keeps that session " +
				"in the system keyring, not the password."})
		if err != nil {
			return api.LoginEnd{}, err
		}
		in, err := m.LoginMatrix(ctx, got["password"])
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

// matrixSetup sets a Matrix account up in a config that names none: its homeserver
// and user are written, and the daemon restarted to run Matrix, whose encryption
// store opens at start; the login then goes on there.
type matrixSetup struct{}

var matrixIDShape = regexp.MustCompile(`^@[^:\s]+:\S+$`)

// LoginNetwork is Matrix, with no account yet.
func (matrixSetup) LoginNetwork() api.LoginNetwork {
	return api.LoginNetwork{Network: "matrix", Label: "Matrix", Detail: "an account on a homeserver"}
}

// Login asks for the homeserver and the Matrix ID, and has them written.
func (matrixSetup) Login(ctx context.Context, _ string, talk api.LoginTalk) (api.LoginEnd, error) {
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
func (m *matrixAdapter) handOver(ctx context.Context, sess domain.Session) (bool, error) {
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

// BackupRoomKeys has nothing to back up before a session (daemon.KeyBackup).
func (m *matrixAdapter) BackupRoomKeys(ctx context.Context) (int, error) {
	if !m.LoggedIn() {
		return 0, nil
	}
	return m.InProc.BackupRoomKeys(ctx) //nolint:wrapcheck // the adapter's own error, passed through
}

// savedSession reads the session saved by `kith login`: zero when there is none.
func savedSession(cfg config.Config, keys session.Store) (domain.Session, error) {
	saved, found, err := session.Load(keys, cfg.AllowTokenFile)
	if err != nil {
		return domain.Session{}, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return domain.Session{}, nil
	}
	return saved, nil
}

// resumeSession restores a session. prepare is the homeserver-dependent startup left
// to do, nil when the homeserver answered. A rejected session is
// api.ErrSessionRejected, and leaves the crypto store unopened.
func resumeSession(
	ctx context.Context, log *slog.Logger, backend *matrix.InProc, crypto cryptoPlace, saved domain.Session,
) (prepare func(context.Context) error, err error) {
	rerr := backend.Resume(ctx, saved)
	if rerr == nil || errors.Is(rerr, api.ErrUnreachable) {
		// Before anything reaches the adapter, and offline too: the state store must be
		// in place before any RPC runs (see matrix.InProc.OpenCryptoStore).
		openCryptoStore(ctx, log, backend, crypto.path)
	}
	switch {
	case rerr == nil:
		enableEncryption(ctx, log, backend, crypto.keys)
		return nil, nil
	case errors.Is(rerr, api.ErrUnreachable):
		// Serve the on-disk cache offline and connect once the homeserver is back.
		log.Warn("cannot reach the homeserver; serving cached history and connecting when it is back", "err", rerr)
		return func(ctx context.Context) error {
			if werr := waitForHomeserver(ctx, backend); werr != nil {
				return werr
			}
			log.Info("homeserver reachable again; syncing")
			enableEncryption(ctx, log, backend, crypto.keys)
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("saved session for %s is unusable (run `kith login` again): %w", saved.UserID, rerr)
	}
}

// waitForHomeserver polls until the homeserver answers, ctx ends, or the session is
// rejected. Backoff is capped at 30s so a laptop waking from suspend reconnects quickly.
func waitForHomeserver(ctx context.Context, backend *matrix.InProc) error {
	const (
		first = 2 * time.Second
		most  = 30 * time.Second
	)
	for wait := first; ; {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the homeserver: %w", ctx.Err())
		case <-time.After(wait):
		}
		switch err := backend.Reachable(ctx); {
		case err == nil:
			return nil
		case errors.Is(err, api.ErrUnreachable):
		default:
			return fmt.Errorf("saved session is unusable (stop kithd, run `kith login` again): %w", err)
		}
		if wait *= 2; wait > most {
			wait = most
		}
	}
}

// openCryptoStore opens the crypto/state store, logging a failure: encryption then
// stays off and encrypted rooms refuse sends.
func openCryptoStore(ctx context.Context, log *slog.Logger, backend *matrix.InProc, dbPath string) {
	if err := backend.OpenCryptoStore(ctx, dbPath); err != nil {
		log.Error("encryption disabled", "err", err)
	}
}

// enableEncryption turns on E2EE. Failures only warn: unencrypted rooms still work.
func enableEncryption(ctx context.Context, log *slog.Logger, backend *matrix.InProc, keys session.Store) {
	pickleKey, err := session.LoadOrCreatePickleKey(keys)
	if err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if err := backend.EnableEncryption(ctx, pickleKey); err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if verr := backend.VerificationUnavailable(); verr != nil {
		log.Warn("device verification unavailable", "err", verr)
	}
}
