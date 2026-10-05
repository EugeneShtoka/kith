// Package telegram is kith's own link to Telegram: each configured account is one phone
// number it logs in as, a Telegram client of its own (through gotd/td, pure Go). It
// writes what it sees into the cache every network shares, so search, drafts, the
// assistant and notifications work for it as they do for Matrix.
package telegram

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each stream, as the other adapters' do.
const streamBuffer = 64

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("telegram: started twice")

// Account is one configured Telegram account: a phone number.
type Account struct {
	// Name is what `kith login telegram <name>` takes.
	Name string
	// Digits is its phone number's digits, as Telegram takes it.
	Digits string
}

// A configured account's state, as every network's account says it.
const (
	// LoggedOut has no credentials: never logged in, or logged out by Telegram.
	LoggedOut = domain.AccountLoggedOut
	// Connecting is logged in and reaching Telegram.
	Connecting = domain.AccountConnecting
	// Connected is logged in and connected.
	Connected = domain.AccountOnline
)

// Adapter is every configured Telegram account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	log     *slog.Logger

	// onStatus hears an account's state change (see OnStatus).
	onStatus func(domain.AccountStatus)

	mu       sync.Mutex
	accounts []Account
	// signIns counts each account's logins, so neither a login nor a connection begun
	// on an older one ever replaces a newer one's credentials or connection.
	signIns map[string]int
	// logins are the logins under way, conns the logged-in accounts' connections, by
	// account name.
	logins  map[string]*login
	conns   map[string]*conn
	started bool
	stopped bool
	// run is Start's context: logins and connections last as long.
	run context.Context //nolint:containedctx // RPCs and reloads arrive with no context of their own to outlive

	// keeping serializes writing credentials with checking that their login is still
	// the latest (keepLogin).
	keeping sync.Mutex

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New is the adapter for accounts, their credentials in secrets, writing into cache.
// log may be nil.
func New(cache *db.Cache, secrets Secrets, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cache: cache, secrets: secrets, accounts: slices.Clone(accounts), log: log.With("network", "telegram"),
		signIns:   map[string]int{},
		logins:    map[string]*login{},
		conns:     map[string]*conn{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Telegram.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolTelegram }

// OnStatus sets who hears an account's state change, with what to tell a person
// about it (empty when nothing). Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// session reports an account's state.
func (a *Adapter) session(account Account, phase domain.AccountPhase, detail string) {
	if a.onStatus != nil {
		a.onStatus(domain.AccountStatus{Network: domain.ProtocolTelegram, Account: account.Name, Phase: phase, Detail: detail})
	}
}

// UseConfig takes [[telegram.account]], at start and each time the config is re-read
// (see useAccounts).
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	accounts := make([]Account, 0, len(cfg.Telegram.Accounts))
	for _, account := range cfg.Telegram.Accounts {
		accounts = append(accounts, Account{Name: account.Name, Digits: domain.PhoneDigits(account.Phone)})
	}
	a.useAccounts(accounts)
}

// CheckConfig refuses [[telegram.account]]s kith could not tell apart or log in as: a
// missing or repeated name, no international number, a number listed twice. It asks
// nothing of Telegram.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Telegram.Accounts))
	for _, account := range cfg.Telegram.Accounts {
		records = append(records, domain.PhoneAccount(account.Name, account.Phone))
	}
	return domain.CheckAccounts(cfg.Telegram.Table(), records) //nolint:wrapcheck // names the record itself
}

// accountsNow is the configured accounts (useAccounts replaces them).
func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// runContext is what logins and connections run under: Start's context, once it ran.
func (a *Adapter) runContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return context.Background()
	}
	return a.run
}

// useAccounts takes the configured accounts: before Start, the ones it starts with;
// after, an account added is connected if logged in, as one configured at Start is,
// and one removed is let go, with its login under way.
func (a *Adapter) useAccounts(accounts []Account) {
	a.mu.Lock()
	known := a.accounts
	a.accounts = slices.Clone(accounts)
	kept := func(name string) bool {
		return slices.ContainsFunc(accounts, func(k Account) bool { return k.Name == name })
	}
	for name, c := range a.conns {
		if !kept(name) {
			c.cancel()
			delete(a.conns, name)
		}
	}
	for name, l := range a.logins {
		if !kept(name) {
			l.cancel()
			delete(a.logins, name)
		}
	}
	started := a.started && !a.stopped
	a.mu.Unlock()
	if !started {
		return
	}
	for _, account := range accounts {
		if !slices.ContainsFunc(known, func(k Account) bool { return k.Name == account.Name }) {
			a.announce(account)
		}
	}
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
		a.announce(account)
	}
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // our own shutdown, as the other adapters'
}

// SavedSessions is the configured accounts that have credentials kept, by name: what
// Start connects. An account whose credentials are unusable is not one (its status
// says why); a store that cannot be read is an error.
func (a *Adapter) SavedSessions(context.Context) ([]string, error) {
	var in []string
	for _, account := range a.accountsNow() {
		_, ok, err := loadCredentials(a.secrets, account.Digits)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			in = append(in, account.Name)
		}
	}
	return in, nil
}

// announce connects an account that is logged in, and says how to log in when it is
// not.
func (a *Adapter) announce(account Account) {
	creds, ok, err := loadCredentials(a.secrets, account.Digits)
	switch {
	case err != nil:
		a.log.Warn("read the Telegram credentials failed", "account", account.Name, "err", err)
		a.session(account, LoggedOut, "its credentials could not be read: "+err.Error())
	case !ok:
		hint := "not logged in; " + loginHint(account)
		a.log.Info(hint, "account", account.Name)
		a.session(account, LoggedOut, hint)
	default:
		a.log.Info("logged in", "account", account.Name, "user", creds.User)
		a.mu.Lock()
		gen := a.signIns[account.Name]
		a.mu.Unlock()
		a.connectAs(account, creds, gen, a.newClient)
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
	for _, c := range a.conns {
		c.cancel()
	}
	for _, l := range a.logins {
		l.cancel()
	}
	a.mu.Unlock()
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.closed = true
	close(a.messages)
	close(a.activity)
	close(a.unread)
	close(a.reactions)
}

// Messages, Activity, Unread and Reactions are the live streams.
func (a *Adapter) Messages() <-chan domain.Message { return a.messages }

// Activity is typing and receipts.
func (a *Adapter) Activity() <-chan domain.Activity { return a.activity }

// Unread is unread counts as they change.
func (a *Adapter) Unread() <-chan domain.Unread { return a.unread }

// Reactions is reactions as they change.
func (a *Adapter) Reactions() <-chan domain.ReactionUpdate { return a.reactions }

// Me is every Telegram ID that is this person: one per connected account.
func (a *Adapter) Me() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var me []string
	for _, c := range a.conns {
		if c.user != 0 {
			me = append(me, personID(c.user))
		}
	}
	slices.Sort(me)
	return me
}

// RoomEncryption: Telegram rooms are not end-to-end encrypted (secret chats, which
// are, are not shown).
func (a *Adapter) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, id := range roomIDs {
		out[id] = false
	}
	return out, nil
}
