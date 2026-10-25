package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// Place is where this instance keeps each Matrix account's secrets, by the account's
// key (domain.AccountKey of its Matrix ID): its encryption store's path, and its
// keyring entries.
type Place struct {
	CryptoPath func(key string) string
	Keys       func(key string) session.Store
}

// Adapter is the Matrix network adapter: every [[matrix.account]], each a runner with
// its own session, device and crypto store, over one cache. Calls go to the account an
// ID names; the router treats Matrix as off until one account is logged in.
type Adapter struct {
	cache *db.Cache
	log   *slog.Logger
	place Place
	hooks *hooks

	// configs runs one UseConfig at a time, so accounts are added and removed in order.
	configs sync.Mutex

	mu sync.Mutex
	// runners are the configured accounts', in config order.
	runners []*runner
	// What every runner takes, a new one too: the config's settings and the hooks
	// set before Start.
	allowFile    bool
	keepDeleted  bool
	bridges      []string
	identities   [][]string
	onSynced     func(domain.RoomOwner, time.Time)
	onRoomsStale func()
	onCached     func(domain.Message)
	onChanged    func(domain.RoomID)
	// child is a context under Start's, which an account added later runs under too;
	// closing is set once it ended, after which no run starts. runs counts the runs going.
	child   func() (context.Context, context.CancelFunc)
	closing bool
	runs    sync.WaitGroup
	// txns is which runner each verification in progress is on.
	txns map[string]*runner

	// out is every account's streams merged; forwards carries them over (merge.go).
	out           streams
	verifications chan domain.Verification
	forwards      sync.WaitGroup
	invites       accountInvites
	stopOnce      sync.Once
}

// NewAdapter is the adapter over cache, its accounts' secrets kept at place. It has no
// account until UseConfig names some.
func NewAdapter(cache *db.Cache, log *slog.Logger, place Place) *Adapter {
	m := &Adapter{
		cache: cache, log: log, place: place, hooks: &hooks{},
		txns:          map[string]*runner{},
		verifications: make(chan domain.Verification, verificationBuffer),
	}
	m.out.open()
	m.invites.by = map[*runner][]domain.Room{}
	return m
}

// Network is Matrix.
func (m *Adapter) Network() domain.Protocol { return domain.ProtocolMatrix }

// OnStatus sets who hears each account's phase change. Set before Start.
func (m *Adapter) OnStatus(changed func(domain.AccountStatus)) { m.hooks.onStatus = changed }

// OnLoggedIn sets who hears a session taking, so what was skipped while logged out is
// caught up. Set before Start.
func (m *Adapter) OnLoggedIn(loggedIn func()) { m.hooks.onLoggedIn = loggedIn }

// Online is account on online, as a sync says it is.
func (m *Adapter) Online(on domain.RoomOwner) domain.AccountStatus {
	for _, r := range m.runnersNow() {
		if r.ids.owner() == on {
			return r.status(domain.AccountOnline, "")
		}
	}
	return domain.AccountStatus{Network: domain.ProtocolMatrix, Phase: domain.AccountOnline}
}

// runnersNow is the configured accounts' runners, in config order.
func (m *Adapter) runnersNow() []*runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.runners)
}

// live is the runners logged in, in config order.
func (m *Adapter) live() []*runner {
	return slices.DeleteFunc(m.runnersNow(), func(r *runner) bool { return !r.loggedIn.Load() })
}

// UseConfig applies the config at start and on each re-read. A new account starts at
// once; one gone, or now on another homeserver, is stopped first; a renamed one keeps
// running under its new name.
func (m *Adapter) UseConfig(ctx context.Context, cfg config.Config) {
	m.configs.Lock()
	defer m.configs.Unlock()
	m.mu.Lock()
	log := m.log
	m.allowFile, m.keepDeleted = cfg.AllowTokenFile, cfg.Display.Deleted.Keep()
	m.bridges, m.identities = slices.Clone(cfg.BridgeContacts), identityGroups(cfg)
	gone, added := m.diff(cfg)
	m.mu.Unlock()

	// Stopped before any is added: an account moved to another homeserver reopens the
	// same crypto store.
	for _, r := range gone {
		m.retire(r)
	}
	for _, acc := range added {
		r := newRunner(m.cache, log, m.place, acc, cfg.AllowTokenFile, m.hooks)
		m.mu.Lock()
		if m.closing {
			m.mu.Unlock()
			r.stop()
			continue
		}
		m.runners = append(m.runners, r)
		m.forward(r)
		m.mu.Unlock()
	}
	m.mu.Lock()
	order := func(r *runner) int {
		return slices.IndexFunc(cfg.Matrix.Accounts, func(a config.MatrixAccount) bool {
			return domain.AccountKey(a.User) == r.ids.account
		})
	}
	slices.SortStableFunc(m.runners, func(a, b *runner) int { return order(a) - order(b) })
	for _, r := range m.runners {
		m.apply(r)
		if !r.launched {
			m.launch(r)
		}
	}
	runners, identities := slices.Clone(m.runners), m.identities
	m.mu.Unlock()
	for _, r := range runners {
		r.UseIdentities(ctx, identities)
	}
}

// diff keeps the runners of accounts cfg still names on the same homeserver, renamed
// as cfg names them, and says which runners go and which accounts are new. Caller
// holds mu.
func (m *Adapter) diff(cfg config.Config) (gone []*runner, added []config.MatrixAccount) {
	var kept []*runner
	for _, acc := range cfg.Matrix.Accounts {
		key := domain.AccountKey(acc.User)
		if slices.ContainsFunc(kept, func(r *runner) bool { return r.ids.account == key }) ||
			slices.ContainsFunc(added, func(a config.MatrixAccount) bool { return domain.AccountKey(a.User) == key }) {
			continue // CheckConfig refuses an account listed twice
		}
		i := slices.IndexFunc(m.runners, func(r *runner) bool {
			return r.ids.account == key && r.account().Homeserver == acc.Homeserver
		})
		if i < 0 {
			added = append(added, acc)
			continue
		}
		m.runners[i].rename(acc.Name, cfg.AllowTokenFile)
		kept = append(kept, m.runners[i])
	}
	for _, r := range m.runners {
		if !slices.Contains(kept, r) {
			gone = append(gone, r)
		}
	}
	m.runners = kept
	return gone, added
}

// identityGroups is each [[display.identity]]'s user IDs.
func identityGroups(cfg config.Config) [][]string {
	groups := make([][]string, 0, len(cfg.Display.Identities))
	for _, ident := range cfg.Display.Identities {
		groups = append(groups, ident.IDs)
	}
	return groups
}

// apply gives r the settings and hooks every runner takes. Caller holds mu.
func (m *Adapter) apply(r *runner) {
	r.KeepDeleted(m.keepDeleted)
	r.useBridgeContacts(m.bridges)
	if !r.launched {
		// Read by the run only, so set before it starts.
		r.onSynced, r.onRoomsStale, r.onCached, r.onChanged = m.onSynced, m.onRoomsStale, m.onCached, m.onChanged
	}
}

// launch starts r's run under Start's context, unless Start has not run yet or has
// ended. Caller holds mu. A failed run keeps r, its cache readable, until it is
// removed or the adapter stops.
func (m *Adapter) launch(r *runner) {
	if m.child == nil || m.closing {
		return
	}
	ctx, cancel := m.child()
	r.launched, r.cancel = true, cancel
	m.runs.Go(func() {
		r.err = r.start(ctx)
		<-ctx.Done()
		r.stop()
	})
}

// retire stops a runner no longer configured and forgets what it was doing.
func (m *Adapter) retire(r *runner) {
	m.mu.Lock()
	launched := r.launched
	for txn, on := range m.txns {
		if on == r {
			delete(m.txns, txn)
		}
	}
	m.mu.Unlock()
	if launched {
		r.cancel()
		<-r.done
	} else {
		r.stop()
	}
	m.dropInvites(r)
}

// SavedSessions is the accounts with a saved session: Start resumes them.
func (m *Adapter) SavedSessions(context.Context) ([]string, error) {
	var out []string
	for _, r := range m.runnersNow() {
		r.mu.Lock()
		if r.saved.AccessToken != "" {
			out = append(out, r.acc.Name)
		}
		r.mu.Unlock()
	}
	return out, nil
}

// LoggedIn reports whether any account has a session the router may use (route.Session).
func (m *Adapter) LoggedIn() bool { return len(m.live()) > 0 }

// Accounts is every configured account, by the AccountKey its rooms are under.
func (m *Adapter) Accounts() []domain.Account {
	runners := m.runnersNow()
	out := make([]domain.Account, 0, len(runners))
	for _, r := range runners {
		out = append(out, domain.Account{Network: domain.ProtocolMatrix, On: r.ids.owner(), Name: r.account().Name})
	}
	return out
}

// CheckConfig refuses a [[matrix.account]] with no name, no Matrix ID, no homeserver
// URL, or a name or Matrix ID listed twice. Offline.
func (m *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	table := cfg.Matrix.Table()
	records := make([]domain.AccountRecord, 0, len(cfg.Matrix.Accounts))
	for i, acc := range cfg.Matrix.Accounts {
		record := domain.AccountRecord{Name: acc.Name, Field: "user", Written: acc.User, Key: domain.AccountKey(acc.User)}
		if !matrixIDShape.MatchString(acc.User) {
			record.Bad = errors.New("is no Matrix ID: write it whole, as @you:matrix.org")
		}
		records = append(records, record)
		if u, err := url.Parse(acc.Homeserver); acc.Name != "" && (err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http")) {
			return fmt.Errorf("config: %s[%d] (%s): homeserver %q is no URL: write the server's address, as https://matrix.org",
				table, i, acc.Name, acc.Homeserver)
		}
	}
	if err := domain.CheckAccounts(table, records); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// errAdapterStartedTwice refuses a second Start.
var errAdapterStartedTwice = errors.New("matrix: the adapter starts once")

// Start runs every account, and each one a reload adds, until ctx ends: an account
// waits for its login, resumes its session and syncs. One failing leaves the others
// running. It blocks, returning what failed once every run has stopped.
func (m *Adapter) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.child != nil {
		m.mu.Unlock()
		return errAdapterStartedTwice
	}
	m.child = func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }
	for _, r := range m.runners {
		m.launch(r)
	}
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	m.runs.Wait()
	var errs []error
	for _, r := range m.runnersNow() {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.account().Name, r.err))
		}
	}
	return errors.Join(errs...)
}

// Stop stops every account, then the merged streams. Idempotent; safe before Start.
func (m *Adapter) Stop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.closing = true
		runners := slices.Clone(m.runners)
		m.mu.Unlock()
		for _, r := range runners {
			if r.launched {
				r.cancel()
				<-r.done
			} else {
				r.stop()
			}
		}
		m.forwards.Wait()
		m.out.close()
		close(m.verifications)
	})
}

// BackupRoomKeys backs up account on's room keys; it has nothing to back up before a
// session (daemon.KeyBackup).
func (m *Adapter) BackupRoomKeys(ctx context.Context, on domain.RoomOwner) (int, error) {
	r, err := m.runnerOn(on)
	if errors.Is(err, api.ErrNetworkOff) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return r.BackupRoomKeys(ctx, on)
}

// KeepDeleted sets [display.deleted] keep on every account.
func (m *Adapter) KeepDeleted(keep bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keepDeleted = keep
	for _, r := range m.runners {
		r.KeepDeleted(keep)
	}
}

// UseIdentities sets which IDs are one person, on every account.
func (m *Adapter) UseIdentities(ctx context.Context, identities [][]string) {
	m.mu.Lock()
	m.identities = identities
	runners := slices.Clone(m.runners)
	m.mu.Unlock()
	for _, r := range runners {
		r.UseIdentities(ctx, identities)
	}
}

// OnSynced registers a callback with the account and each sync response's arrival
// time. Call before Start; it runs on a sync goroutine and must not block.
func (m *Adapter) OnSynced(fn func(domain.RoomOwner, time.Time)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSynced = fn
	for _, r := range m.runners {
		r.OnSynced(fn)
	}
}

// OnRoomsStale registers a callback for syncs that change rooms, names, spaces or
// m.direct, on any account. Call before Start; it must not block.
func (m *Adapter) OnRoomsStale(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRoomsStale = fn
	for _, r := range m.runners {
		r.OnRoomsStale(fn)
	}
}

// OnCached registers who hears a message cached, and a room's messages otherwise
// changed, on any account. Call before Start.
func (m *Adapter) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onCached, m.onChanged = cached, changed
	for _, r := range m.runners {
		r.OnCached(cached, changed)
	}
}

// UseLogger sets the log; each account's lines name it.
func (m *Adapter) UseLogger(log *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = log
}
