// Package telegram is kith's Telegram client (gotd/td): one login per configured phone
// number, writing into the shared cache like every other network.
package telegram

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each stream.
const streamBuffer = 64

var errStartedTwice = errors.New("telegram: started twice")

// Account is one configured Telegram account: a phone number.
type Account struct {
	// Name is what `kith login telegram <name>` takes.
	Name   string
	Digits string
}

// Account states.
const (
	// LoggedOut has no credentials: never logged in, or logged out by Telegram.
	LoggedOut  = domain.AccountLoggedOut
	Connecting = domain.AccountConnecting
	Connected  = domain.AccountOnline
)

// Adapter is every configured Telegram account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	// store keeps updates state and access hashes; nil keeps them in memory.
	store *Store
	log   *slog.Logger

	onCached  func(domain.Message)
	onChanged func(domain.RoomID)

	// reads is the read marks Telegram has not taken yet (unread.go).
	reads owed

	onStatus       func(domain.AccountStatus)
	onRoomsChanged func()

	mu       sync.Mutex
	accounts []Account
	// signIns is a generation per account, so work from an older login never replaces
	// a newer login's credentials or connection.
	signIns map[string]int
	// logins in progress and live connections, by account name.
	logins map[string]*login
	conns  map[string]*conn
	// keepDeleted is [display.deleted] keep.
	keepDeleted bool
	// selves is each account's user ID; outlives the connection.
	selves  map[string]int64
	started bool
	stopped bool
	// run is Start's context; logins and connections live under it.
	run context.Context //nolint:containedctx // RPCs and reloads have no context to outlive

	// refreshing serializes listings, so an older one never overwrites a newer.
	refreshing sync.Mutex
	// listing keeps listing writes and message writes from interleaving; heard is when
	// each room last had a message cached (keptRooms).
	listing sync.Mutex
	heard   map[domain.RoomID]time.Time
	// moved is when each chat last changed folder live (listedArchive).
	moved map[domain.RoomID]time.Time

	// unreadMu guards unread changes; unreadState is Telegram's unread per room
	// (lazily loaded from the cache), unreadAt when it last changed live.
	unreadMu    sync.Mutex
	unreadState map[domain.RoomID]domain.Unread
	unreadAt    map[domain.RoomID]time.Time

	// reacted is when each message's reactions last changed live (cachePage); typing
	// expires each typist by timer.
	reacted map[domain.EventID]time.Time
	typing  map[domain.RoomID]map[string]*time.Timer
	// echoes are reactions applied from our own sends' answers, until heard again.
	echoes map[string]time.Time
	// notMember is, per account, the channels it has left (following.go).
	notMember map[int64]map[int64]bool

	// keeping makes the latest-login check and the credentials write atomic (keepLogin).
	keeping sync.Mutex

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New builds the adapter. store and log may be nil.
func New(cache *db.Cache, secrets Secrets, store *Store, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cache: cache, secrets: secrets, store: store, accounts: slices.Clone(accounts), log: log.With("network", "telegram"),
		heard:     map[domain.RoomID]time.Time{},
		unreadAt:  map[domain.RoomID]time.Time{},
		moved:     map[domain.RoomID]time.Time{},
		reacted:   map[domain.EventID]time.Time{},
		echoes:    map[string]time.Time{},
		notMember: map[int64]map[int64]bool{},
		typing:    map[domain.RoomID]map[string]*time.Timer{},
		signIns:   map[string]int{},
		logins:    map[string]*login{},
		conns:     map[string]*conn{},
		selves:    map[string]int64{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Telegram.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolTelegram }

// OnCached sets callbacks for each cached message and each room whose cache changed
// otherwise. Set before Start.
func (a *Adapter) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	a.onCached, a.onChanged = cached, changed
}

// OnRoomsChanged sets a callback for rewritten account rooms. Set before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// OnStatus sets a callback for account state changes. Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// session reports an account's state.
func (a *Adapter) session(account Account, phase domain.AccountPhase, detail string) {
	if a.onStatus != nil {
		a.onStatus(domain.AccountStatus{Network: domain.ProtocolTelegram, Account: account.Name, Phase: phase, Detail: detail})
	}
}

// UseConfig applies [[telegram.account]], at start and on each reload.
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	accounts := make([]Account, 0, len(cfg.Telegram.Accounts))
	for _, account := range cfg.Telegram.Accounts {
		accounts = append(accounts, Account{Name: account.Name, Digits: domain.PhoneDigits(account.Phone)})
	}
	a.useAccounts(accounts)
	a.keepDeletedIf(cfg.Display.Deleted.Keep())
}

// CheckConfig rejects missing or duplicate names, non-international and duplicate
// numbers. Offline.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Telegram.Accounts))
	for _, account := range cfg.Telegram.Accounts {
		records = append(records, domain.PhoneAccount(account.Name, account.Phone))
	}
	return domain.CheckAccounts(cfg.Telegram.Table(), records) //nolint:wrapcheck // names the record itself
}

func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// runContext is Start's context, or Background before Start.
func (a *Adapter) runContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return context.Background()
	}
	return a.run
}

// useAccounts replaces the accounts. After Start, an added account is connected if
// logged in; a removed one's connection and login are canceled.
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
	return ctx.Err() //nolint:wrapcheck // our own shutdown
}

// SavedSessions names the accounts with usable stored credentials. Unusable ones are
// skipped (their status says why); an unreadable store is an error.
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

// announce connects a logged-in account, or reports how to log in.
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

// Messages is the live message stream.
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

// RoomEncryption is false for all: secret chats, the only E2E ones, aren't shown.
func (a *Adapter) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, id := range roomIDs {
		out[id] = false
	}
	return out, nil
}
