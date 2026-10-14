// Package meta connects kith to Meta's Messenger, signed in as the Messenger Lite app
// is, over mautrix-meta's messagix library in-process (no bridge). Each configured
// account is one Meta account. It writes into the shared cache, so search, drafts,
// the assistant and notifications work as for Matrix.
package meta

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
	zerologlog "github.com/rs/zerolog/log"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// streamBuffer bounds each live stream.
const streamBuffer = 64

// phaseNames are the account states as the log says them.
var phaseNames = map[domain.AccountPhase]string{
	SignedOut: "signed out", Connecting: "connecting", Connected: "connected", domain.AccountFailed: "failed",
}

// quietGlobalLog points zerolog's global logger, which messagix logs to, at kith's log
// once per process.
var quietGlobalLog sync.Once

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("meta: started twice")

// Account is one configured Messenger account: a Meta account.
type Account struct {
	// Name is what `kith login messenger <name>` takes.
	Name string
}

// A configured account's state.
const (
	// SignedOut has no session: never signed in, or signed out by Meta.
	SignedOut = domain.AccountLoggedOut
	// Connecting is signed in and reaching Meta.
	Connecting = domain.AccountConnecting
	// Connected is signed in and connected.
	Connected = domain.AccountOnline
)

// Adapter is every configured Messenger account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	log     *slog.Logger

	// onStatus hears an account's state change (see OnStatus).
	onStatus func(domain.AccountStatus)

	mu       sync.Mutex
	accounts []Account
	// conns are the signed-in accounts' connections, by account name.
	conns map[string]*conn
	// signIns counts each account's sign-ins, so a connection begun on an older session
	// never replaces a newer one (see adopt).
	signIns map[string]int
	started bool
	stopped bool
	// run is Start's context; an account added later connects under it.
	run context.Context //nolint:containedctx // reloads arrive with no context of their own to outlive

	// keeping serializes writing sessions to the secret store: a login's and a
	// connection's rotated cookies, so an older session never overwrites a newer one.
	keeping sync.Mutex

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New is the adapter for accounts, their sessions in secrets, writing into cache. log
// may be nil.
func New(cache *db.Cache, secrets Secrets, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	quietGlobalLog.Do(func() {
		// messagix logs its parse warnings to zerolog's global logger, as raw JSON on
		// stderr, dozens for each inbox: they go through kith's log, errors only.
		zerologlog.Logger = logging.Zerolog(log.With("network", "messenger", "component", "messagix"), zerolog.ErrorLevel)
	})
	return &Adapter{
		cache: cache, secrets: secrets, accounts: slices.Clone(accounts), log: log.With("network", "messenger"),
		conns:     map[string]*conn{},
		signIns:   map[string]int{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Messenger.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolMessenger }

// OnStatus sets who hears an account's state change, with a detail for the person
// (empty when none). Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// UseConfig takes [[messenger.account]], at start and on reload.
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	accounts := make([]Account, 0, len(cfg.Messenger.Accounts))
	for _, account := range cfg.Messenger.Accounts {
		accounts = append(accounts, Account{Name: account.Name})
	}
	a.useAccounts(accounts)
}

// CheckConfig refuses [[messenger.account]]s with a missing or repeated name. It does
// not contact Meta.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Messenger.Accounts))
	for _, account := range cfg.Messenger.Accounts {
		// Which Meta account it is comes from the login, so the name is its only key.
		records = append(records, domain.AccountRecord{Name: account.Name, Key: account.Name})
	}
	return domain.CheckAccounts(cfg.Messenger.Table(), records) //nolint:wrapcheck // names the record itself
}

func (a *Adapter) session(account Account, phase domain.AccountPhase, detail string) {
	a.log.Info("account state", "account", account.Name, "phase", phaseNames[phase], "detail", detail)
	if a.onStatus != nil {
		a.onStatus(domain.AccountStatus{Network: domain.ProtocolMessenger, Account: account.Name, Phase: phase, Detail: detail})
	}
}

func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// useAccounts replaces the configured accounts; once started, an added signed-in
// account connects and a removed one is let go.
func (a *Adapter) useAccounts(accounts []Account) {
	a.mu.Lock()
	known := a.accounts
	a.accounts = slices.Clone(accounts)
	var gone []*conn
	for name, c := range a.conns {
		if !slices.ContainsFunc(accounts, func(k Account) bool { return k.Name == name }) {
			gone = append(gone, c)
			delete(a.conns, name)
		}
	}
	started, ctx := a.started && !a.stopped, a.run
	a.mu.Unlock()
	for _, c := range gone {
		c.close()
	}
	if !started {
		return
	}
	for _, account := range accounts {
		if !slices.ContainsFunc(known, func(k Account) bool { return k.Name == account.Name }) {
			a.announce(ctx, account)
		}
	}
}

// SavedSessions is the configured accounts with a stored session. Unusable sessions
// are skipped (their status says why); an unreadable store is an error.
func (a *Adapter) SavedSessions(context.Context) ([]string, error) {
	var signed []string
	for _, account := range a.accountsNow() {
		_, ok, err := loadSession(a.secrets, account.Name)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			signed = append(signed, account.Name)
		}
	}
	return signed, nil
}

// Start reports each account's state and runs until ctx ends or Stop is called.
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errStartedTwice
	}
	a.started, a.run = true, ctx
	a.mu.Unlock()
	for _, account := range a.accountsNow() {
		a.announce(ctx, account)
	}
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // our own shutdown, as the other adapters'
}

// announce connects a signed-in account, or reports how to sign in.
func (a *Adapter) announce(ctx context.Context, account Account) {
	s, ok, err := loadSession(a.secrets, account.Name)
	switch {
	case err != nil:
		a.log.Warn("read the Messenger session failed", "account", account.Name, "err", err)
		a.session(account, SignedOut, "its session could not be read: "+err.Error())
	case !ok:
		hint := "not signed in; run `kith login messenger " + account.Name + "`"
		a.log.Info(hint, "account", account.Name)
		a.session(account, SignedOut, hint)
	default:
		a.log.Info("signed in", "account", account.Name, "meta_id", s.MetaID)
		c := a.newConn(account, s, nil)
		if a.adopt(c) {
			go a.connect(ctx, c, nil)
		}
	}
}

// Stop ends every account's connection and closes the streams.
func (a *Adapter) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	conns := make([]*conn, 0, len(a.conns))
	for _, c := range a.conns {
		conns = append(conns, c)
	}
	a.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.closed = true
	close(a.messages)
	close(a.activity)
	close(a.unread)
	close(a.reactions)
}

// Messages is the live message stream.
func (a *Adapter) Messages() <-chan domain.Message { return a.messages }

// Activity is typing and receipts.
func (a *Adapter) Activity() <-chan domain.Activity { return a.activity }

// Unread is unread counts as they change.
func (a *Adapter) Unread() <-chan domain.Unread { return a.unread }

// Reactions is reactions as they change.
func (a *Adapter) Reactions() <-chan domain.ReactionUpdate { return a.reactions }

// Me is every Messenger ID that is this person: one per signed-in account.
func (a *Adapter) Me() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	me := make([]string, 0, len(a.conns))
	for _, c := range a.conns {
		me = append(me, personID(c.metaID))
	}
	slices.Sort(me)
	return slices.Compact(me)
}

// personID is a Meta user's kith ID.
func personID(metaID int64) string {
	return domain.NativePerson(domain.ProtocolMessenger, strconv.FormatInt(metaID, 10))
}
