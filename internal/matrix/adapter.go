package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// Account is the config's Matrix account and where its secrets are kept.
type Account struct {
	Homeserver, User string
	AllowTokenFile   bool
	Crypto           CryptoPlace
}

// CryptoPlace is where the Matrix encryption store and its key are kept.
type CryptoPlace struct {
	Path string
	Keys session.Store
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

// Adapter is the Matrix adapter as the router starts it. It may start with no
// session, or one the homeserver rejects: Start then waits for passwordLogin, and the
// router treats Matrix as off until LoggedIn. What is left of connecting once the
// homeserver answers belongs to its Start, and holds up no other network.
type Adapter struct {
	*InProc
	log     *slog.Logger
	account Account

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

// NewAdapter is the adapter for account over cache, starting from saved (zero
// when there is no saved session).
func NewAdapter(cache *db.Cache, log *slog.Logger, account Account, saved domain.Session) *Adapter {
	log = log.With("user", account.User)
	m := &Adapter{
		InProc: New(cache), log: log, account: account,
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
func (m *Adapter) Network() domain.Protocol { return domain.ProtocolMatrix }

// OnStatus sets who hears the account's phase change. Set before Start.
func (m *Adapter) OnStatus(changed func(domain.AccountStatus)) { m.onStatus = changed }

// OnLoggedIn sets who hears the session taking, so what was skipped while logged out
// is caught up. Set before Start.
func (m *Adapter) OnLoggedIn(loggedIn func()) { m.onLoggedIn = loggedIn }

// Online is the account online, as a sync says it is.
func (m *Adapter) Online() domain.AccountStatus { return m.status(domain.AccountOnline, "") }

// status is the account in phase, saying detail.
func (m *Adapter) status(phase domain.AccountPhase, detail string) domain.AccountStatus {
	return domain.AccountStatus{Network: domain.ProtocolMatrix, Account: m.account.User, Phase: phase, Detail: detail}
}

// report tells the listener the account's phase changed.
func (m *Adapter) report(phase domain.AccountPhase, detail string) {
	m.onStatus(m.status(phase, detail))
}

// UseConfig takes [display.deleted] keep and the identities, at start and each time
// the config is re-read. The account itself is the daemon's to change (a restart).
func (m *Adapter) UseConfig(ctx context.Context, cfg config.Config) {
	m.KeepDeleted(cfg.Display.Deleted.Keep())
	m.UseIdentities(ctx, identityGroups(cfg))
}

// identityGroups is each [[display.identity]]'s user IDs.
func identityGroups(cfg config.Config) [][]string {
	groups := make([][]string, 0, len(cfg.Display.Identities))
	for _, ident := range cfg.Display.Identities {
		groups = append(groups, ident.IDs)
	}
	return groups
}

// SavedSessions is the account when a session was saved: Start resumes it.
func (m *Adapter) SavedSessions(context.Context) ([]string, error) {
	if m.saved.AccessToken == "" {
		return nil, nil
	}
	return []string{m.account.User}, nil
}

// LoggedIn reports whether Matrix has a session the router may use (route.Session).
func (m *Adapter) LoggedIn() bool { return m.loggedIn.Load() }

// hint is what to do to log Matrix in.
const loginHint = "run `kith login`"

// Start tries the saved session, else waits for a login; then finishes connecting
// and runs the sync loop. A rejected session waits for a login again. It blocks.
func (m *Adapter) Start(ctx context.Context) error {
	next := m.saved
	for {
		if next.AccessToken == "" {
			m.report(domain.AccountLoggedOut, "no saved session; "+loginHint)
			var ok bool
			if next, ok = m.takeLogin(ctx); !ok {
				return nil // shut down while logged out
			}
		}
		m.report(domain.AccountConnecting, "resuming the session")
		prepare, err := resumeSession(ctx, m.log, m.InProc, m.account.Crypto, next)
		if errors.Is(err, api.ErrSessionRejected) {
			m.log.Warn("the saved Matrix session was rejected; waiting for `kith login`", "err", err)
			m.report(domain.AccountLoggedOut, "the homeserver rejected the session; "+loginHint)
			m.settle(awaitingLogin)
			next = domain.Session{}
			continue
		}
		if err != nil {
			m.report(domain.AccountFailed, err.Error())
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

// takeLogin waits for passwordLogin to hand a session over (one may already have).
func (m *Adapter) takeLogin(ctx context.Context) (domain.Session, bool) {
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
func (m *Adapter) enterResuming() {
	m.phase = resuming
	m.settled = make(chan struct{})
}

// settle ends a resuming phase in p, waking the logins waiting to learn the outcome.
func (m *Adapter) settle(p sessionPhase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase == resuming {
		close(m.settled)
	}
	m.phase = p
}

// run finishes connecting (prepare, nil when the homeserver answered), then syncs.
func (m *Adapter) run(ctx context.Context, prepare func(context.Context) error) error {
	if prepare != nil {
		m.report(domain.AccountConnecting, "the homeserver is unreachable; serving cached history")
		if err := domain.SyncFault(ctx, prepare(ctx)); err != nil {
			m.report(domain.AccountFailed, err.Error())
			return fmt.Errorf("connect: %w", err)
		}
	}
	if err := m.InProc.Start(ctx); err != nil {
		if fault := domain.SyncFault(ctx, err); fault != nil {
			m.report(domain.AccountFailed, fault.Error())
		}
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// BackupRoomKeys has nothing to back up before a session (daemon.KeyBackup).
func (m *Adapter) BackupRoomKeys(ctx context.Context) (int, error) {
	if !m.LoggedIn() {
		return 0, nil
	}
	return m.InProc.BackupRoomKeys(ctx)
}
