package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// account is a configured Matrix account and where its secrets are kept.
type account struct {
	Name, Homeserver, User string
	AllowTokenFile         bool
	Crypto                 cryptoPlace
}

// cryptoPlace is where an account's encryption store and its key are kept.
type cryptoPlace struct {
	Path string
	Keys session.Store
}

// sessionPhase is where a runner is with its session.
type sessionPhase int

const (
	// awaitingLogin has no usable session: a login hands one over.
	awaitingLogin sessionPhase = iota
	// resuming is trying a session; a rejected one goes back to awaitingLogin.
	resuming
	// running has opened the crypto store on a session: a login now is saved for the
	// next start, as a second session cannot share the store.
	running
	// stopped is a runner removed by a reload or stopped with the adapter: a login now
	// is saved for whichever runner the account has next.
	stopped
)

// hooks are who hears every account's phases and logins. Set before Start.
type hooks struct {
	onStatus   func(domain.AccountStatus)
	onLoggedIn func()
}

// runner is one configured Matrix account: its session, whose phases it reports, and
// the backend that syncs it. The adapter routes each call to its account's runner.
type runner struct {
	*InProc
	log   *slog.Logger // names the account
	hooks *hooks

	// loggedIn turns true once the crypto store is open on a session, before anything
	// reaches the runner through the adapter.
	loggedIn atomic.Bool

	// logins runs one login at a time: the session saved last is the newest login's.
	logins sync.Mutex

	mu  sync.Mutex
	acc account
	// saved is the session to try first; zero when there is none.
	saved domain.Session
	phase sessionPhase
	// pending is the session a login handed over, until start takes it; wake says one
	// was (it holds at most one signal).
	pending *domain.Session
	wake    chan struct{}
	// settled is closed when a resuming phase ends; a login waits on it to learn
	// whether its session is wanted.
	settled chan struct{}

	// contacts is the bridges whose contact lists name numbers (bridgecontacts.go);
	// background is the reads of them, joined before the backend stops.
	contacts   bridgeContacts
	background sync.WaitGroup

	// launched says Adapter.launch started the run, which cancel ends. done is closed
	// once the runner has stopped, its backend too; err is why its run failed.
	launched bool
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
}

// newRunner is the runner of the config's account acc over cache, its secrets kept at
// place, with the session saved for it, if any.
func newRunner(cache *db.Cache, log *slog.Logger, place Place, acc config.MatrixAccount, allowFile bool, h *hooks) *runner {
	key := domain.AccountKey(acc.User)
	keys := place.Keys(key)
	r := &runner{
		InProc: New(cache), log: log.With("account", acc.Name, "user", acc.User), hooks: h,
		acc: account{
			Name: acc.Name, Homeserver: acc.Homeserver, User: acc.User, AllowTokenFile: allowFile,
			Crypto: cryptoPlace{Path: place.CryptoPath(key), Keys: keys},
		},
		phase: awaitingLogin, wake: make(chan struct{}, 1),
		contacts: bridgeContacts{changed: make(chan struct{}, 1)},
		done:     make(chan struct{}),
	}
	saved, err := SavedSession(keys, allowFile)
	if err != nil {
		r.log.Warn("read the saved Matrix session failed; waiting for a login", "err", err)
	}
	r.saved = saved
	r.useAccount(acc.User)
	r.UseLogger(r.log)
	// Still trying, but said where it is seen rather than only logged.
	r.onSyncTrouble = func(detail string) { r.say(domain.AccountConnecting, detail) }
	if saved.AccessToken != "" {
		r.enterResuming()
	}
	return r
}

// account is the runner's account as configured now.
func (r *runner) account() account {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acc
}

// rename takes a re-read config's name for the same account, and its file fallback.
func (r *runner) rename(name string, allowFile bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acc.Name, r.acc.AllowTokenFile = name, allowFile
}

// status is the account in phase, saying detail.
func (r *runner) status(phase domain.AccountPhase, detail string) domain.AccountStatus {
	return domain.AccountStatus{Network: domain.ProtocolMatrix, Account: r.account().Name, Phase: phase, Detail: detail}
}

// say tells the listener the account's phase changed.
func (r *runner) say(phase domain.AccountPhase, detail string) {
	if r.hooks.onStatus != nil {
		r.hooks.onStatus(r.status(phase, detail))
	}
}

// loginHint says how to log the account in.
func (r *runner) loginHint() string {
	name := r.account().Name
	return ":login matrix " + name + " in kith, or run `kith login matrix " + name + "`"
}

// start resumes the saved session or waits for a login, then runs the sync loop. A
// rejected session waits for a login again. It blocks until ctx ends or the sync fails.
func (r *runner) start(ctx context.Context) error {
	r.mu.Lock()
	next, acc := r.saved, r.acc
	r.mu.Unlock()
	for {
		if next.AccessToken == "" {
			r.say(domain.AccountLoggedOut, "no saved session; "+r.loginHint())
			var ok bool
			if next, ok = r.takeLogin(ctx); !ok {
				return nil // shut down, or removed, while logged out
			}
		}
		r.say(domain.AccountConnecting, "resuming the session")
		prepare, err := resumeSession(ctx, r.log, r.InProc, acc.Crypto, next, r.saveRefreshed(next))
		if errors.Is(err, api.ErrSessionRejected) {
			r.log.Warn("the saved Matrix session was rejected; waiting for a login", "err", err)
			r.say(domain.AccountLoggedOut, "the homeserver rejected the session; "+r.loginHint())
			r.settle(awaitingLogin)
			next = domain.Session{}
			continue
		}
		if err != nil {
			r.say(domain.AccountFailed, err.Error())
			// The store may be open: no session can be tried in this process again.
			r.settle(running)
			return err
		}
		r.loggedIn.Store(true)
		r.settle(running)
		if r.hooks.onLoggedIn != nil {
			r.hooks.onLoggedIn()
		}
		r.background.Go(func() { r.followBridgeContacts(ctx) })
		return r.run(ctx, prepare)
	}
}

// saveRefreshed returns a callback mautrix calls each time it refreshes a
// near-expired OAuth access token. The closure keeps the base session fields (which
// never change on refresh) and writes a new session into the keyring under the same
// account, so the next kithd restart resumes with a current token. nil for a session
// without a refresh token: Resume will not wire it in and mautrix will not call it.
func (r *runner) saveRefreshed(base domain.Session) func(context.Context, string, string, time.Time) error {
	if base.RefreshToken == "" {
		return nil
	}
	return func(_ context.Context, refresh, access string, expiry time.Time) error {
		next := base
		next.AccessToken, next.RefreshToken, next.AccessTokenExpiresAt = access, refresh, expiry
		acc := r.account()
		return session.Save(acc.Crypto.Keys, next, acc.AllowTokenFile)
	}
}

// takeLogin waits for a login to hand a session over (one may already have).
func (r *runner) takeLogin(ctx context.Context) (domain.Session, bool) {
	for {
		r.mu.Lock()
		if s := r.pending; s != nil {
			r.pending = nil
			r.mu.Unlock()
			return *s, true
		}
		r.mu.Unlock()
		select {
		case <-r.wake:
		case <-ctx.Done():
			return domain.Session{}, false
		}
	}
}

// enterResuming starts a resuming phase. Caller holds mu, or nothing runs yet.
func (r *runner) enterResuming() {
	r.phase = resuming
	r.settled = make(chan struct{})
}

// settle ends a resuming phase in p, waking the logins waiting to learn the outcome.
func (r *runner) settle(p sessionPhase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == resuming {
		close(r.settled)
	}
	r.phase = p
}

// run finishes connecting (prepare, nil when the homeserver answered), then syncs.
func (r *runner) run(ctx context.Context, prepare func(context.Context) error) error {
	if prepare != nil {
		r.say(domain.AccountConnecting, "the homeserver is unreachable; serving cached history")
		if err := domain.SyncFault(ctx, prepare(ctx)); err != nil {
			r.say(domain.AccountFailed, err.Error())
			return fmt.Errorf("connect: %w", err)
		}
	}
	if err := r.Start(ctx); err != nil {
		if fault := domain.SyncFault(ctx, err); fault != nil {
			r.say(domain.AccountFailed, fault.Error())
		}
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// stop releases the backend once nothing else uses it: the run is over (or never
// started) and the bridge reads are joined. Then done closes.
func (r *runner) stop() {
	r.settle(stopped) // a login waiting on its resume learns it is over
	r.background.Wait()
	r.Stop()
	close(r.done)
}

// handOver gives a waiting runner the session, reporting whether it took it. A runner
// trying another session is waited for, in case that one is rejected.
func (r *runner) handOver(ctx context.Context, sess domain.Session) (bool, error) {
	for {
		r.mu.Lock()
		switch r.phase {
		case awaitingLogin:
			r.pending = &sess
			r.enterResuming()
			r.mu.Unlock()
			select {
			case r.wake <- struct{}{}:
			default: // a signal is already there; start takes the pending session
			}
			return true, nil
		case running, stopped:
			r.mu.Unlock()
			return false, nil
		case resuming:
			settled := r.settled
			r.mu.Unlock()
			select {
			case <-settled:
			case <-ctx.Done():
				return false, fmt.Errorf("saved, but Matrix is still connecting on another session: %w", ctx.Err())
			}
		}
	}
}
