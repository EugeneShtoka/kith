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

// Place is where this instance keeps a Matrix account's secrets: the encryption
// store's path, and the keyring entries for a user.
type Place struct {
	CryptoPath string
	Keys       func(user string) session.Store
}

// account is the config's Matrix account and where its secrets are kept.
type account struct {
	Homeserver, User string
	AllowTokenFile   bool
	Crypto           cryptoPlace
}

// cryptoPlace is where the Matrix encryption store and its key are kept.
type cryptoPlace struct {
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

// Adapter is the Matrix adapter as the router starts it. It is built with no account:
// the config gives it one (UseConfig, at start or when re-read), and Start waits for
// it. It may then have no session, or one the homeserver rejects: Start waits for
// passwordLogin, and the router treats Matrix as off until LoggedIn. What is left of
// connecting once the homeserver answers belongs to its Start, and holds up no other
// network.
type Adapter struct {
	*InProc
	log   *slog.Logger
	place Place

	// onStatus hears each phase change (the daemon's per-network status); onLoggedIn
	// hears the session taking, so what was skipped while logged out is caught up.
	// Set before Start.
	onStatus   func(domain.AccountStatus)
	onLoggedIn func()

	// loggedIn turns true once the crypto store is open on a session, before
	// anything reaches the adapter through the router.
	loggedIn atomic.Bool

	// logins runs one login at a time: the session saved last is the newest login's.
	logins sync.Mutex

	mu sync.Mutex
	// account is the config's, once it named one; accounted is closed then. It is
	// taken once: the encryption store opens for one user, so another takes a restart.
	account   account
	accounted chan struct{}
	// saved is the session to try first; zero when there is none. userLog is log
	// naming the account. log itself never changes, so it is read unlocked.
	saved   domain.Session
	userLog *slog.Logger
	phase   sessionPhase
	// pending is the session a login handed over, until Start takes it; wake says
	// one was (it holds at most one signal).
	pending *domain.Session
	wake    chan struct{}
	// settled is closed when a resuming phase ends; a login waits on it to learn
	// whether its session is wanted.
	settled chan struct{}
	// contacts is the bridges whose contact lists name numbers (bridgecontacts.go).
	contacts bridgeContacts
}

// NewAdapter is the adapter over cache, its secrets kept at place. It has no account
// until UseConfig gives it one.
func NewAdapter(cache *db.Cache, log *slog.Logger, place Place) *Adapter {
	m := &Adapter{
		InProc: New(cache), log: log, place: place,
		onStatus: func(domain.AccountStatus) {}, onLoggedIn: func() {},
		accounted: make(chan struct{}), wake: make(chan struct{}, 1),
		contacts: bridgeContacts{changed: make(chan struct{}, 1)},
	}
	m.phase = awaitingLogin
	m.UseLogger(log)
	return m
}

// accountNow is the account, zero before the config named one.
func (m *Adapter) accountNow() account {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.account
}

// takeAccount takes the config's account, with the session saved for it, unless one
// was taken already: then a different one waits for a restart, said in the log.
func (m *Adapter) takeAccount(cfg config.Config) {
	have := m.accountNow()
	switch {
	case have.User != "" && (have.User != cfg.User || have.Homeserver != cfg.Homeserver):
		m.log.Warn("the config names another Matrix account; kithd runs it from its next start",
			"running", have.User, "config", cfg.User)
		return
	case have.User != "" || !cfg.HasMatrix():
		return
	}
	keys := m.place.Keys(cfg.User)
	saved, err := SavedSession(cfg, keys)
	if err != nil {
		m.log.Warn("read the saved Matrix session failed; waiting for a login", "user", cfg.User, "err", err)
	}
	log := m.log.With("user", cfg.User)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.account.User != "" {
		return // taken meanwhile by a re-read racing this one
	}
	m.account = account{
		Homeserver: cfg.Homeserver, User: cfg.User, AllowTokenFile: cfg.AllowTokenFile,
		Crypto: cryptoPlace{Path: m.place.CryptoPath, Keys: keys},
	}
	m.saved, m.userLog = saved, log
	m.UseLogger(log)
	if saved.AccessToken != "" {
		m.enterResuming()
	}
	close(m.accounted)
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
	return domain.AccountStatus{Network: domain.ProtocolMatrix, Account: m.accountNow().User, Phase: phase, Detail: detail}
}

// report tells the listener the account's phase changed.
func (m *Adapter) report(phase domain.AccountPhase, detail string) {
	m.onStatus(m.status(phase, detail))
}

// UseConfig takes the account, [display.deleted] keep and the identities, at start and
// each time the config is re-read. An account set up while kithd runs starts at once;
// one changed waits for a restart (takeAccount).
func (m *Adapter) UseConfig(ctx context.Context, cfg config.Config) {
	m.KeepDeleted(cfg.Display.Deleted.Keep())
	m.UseIdentities(ctx, identityGroups(cfg))
	m.useBridgeContacts(cfg.BridgeContacts)
	m.takeAccount(cfg)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saved.AccessToken == "" {
		return nil, nil
	}
	return []string{m.account.User}, nil
}

// LoggedIn reports whether Matrix has a session the router may use (route.Session).
func (m *Adapter) LoggedIn() bool { return m.loggedIn.Load() }

// hint is what to do to log Matrix in.
const loginHint = ":login matrix in kith, or run `kith login`"

// Start waits for the config to name the account, then tries the saved session, else
// waits for a login; then finishes connecting and runs the sync loop. A rejected
// session waits for a login again. It blocks.
func (m *Adapter) Start(ctx context.Context) error {
	select {
	case <-m.accounted:
	case <-ctx.Done():
		return nil // shut down with no account
	}
	m.mu.Lock()
	next, acc, log := m.saved, m.account, m.userLog
	m.mu.Unlock()
	for {
		if next.AccessToken == "" {
			m.report(domain.AccountLoggedOut, "no saved session; "+loginHint)
			var ok bool
			if next, ok = m.takeLogin(ctx); !ok {
				return nil // shut down while logged out
			}
		}
		m.report(domain.AccountConnecting, "resuming the session")
		prepare, err := resumeSession(ctx, log, m.InProc, acc.Crypto, next)
		if errors.Is(err, api.ErrSessionRejected) {
			log.Warn("the saved Matrix session was rejected; waiting for `kith login`", "err", err)
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
		go m.followBridgeContacts(ctx)
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
