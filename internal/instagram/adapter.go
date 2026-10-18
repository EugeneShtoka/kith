// Package instagram connects kith to Instagram direct messages through mautrix-meta's
// instameow, in-process (no bridge), each configured account signed in with an
// instagram.com session's cookies. (Instagram's password sign-in reaches the account
// but stops at a cookie-consent page instameow cannot answer.)
package instagram

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// streamBuffer bounds each live stream.
const streamBuffer = 64

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("instagram: started twice")

// Account is one configured Instagram account.
type Account struct {
	// Name is what `kith login instagram <name>` takes.
	Name string
}

// A configured account's state.
const (
	// SignedOut has no session: never signed in, or the session ended.
	SignedOut = domain.AccountLoggedOut
	// Connecting is signed in and reaching Instagram.
	Connecting = domain.AccountConnecting
	// Connected is signed in and connected.
	Connected = domain.AccountOnline
)

// phaseNames are the account states as the log says them.
var phaseNames = map[domain.AccountPhase]string{
	SignedOut: "signed out", Connecting: "connecting", Connected: "connected", domain.AccountFailed: "failed",
}

// Adapter is every configured Instagram account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	log     *slog.Logger

	// onStatus hears an account's state change; said is each account's last, so a state
	// is said once, not again on every config re-read.
	onStatus func(domain.AccountStatus)
	saidMu   sync.Mutex
	said     map[string]domain.AccountStatus
	// onRoomsChanged hears an account's rooms being rewritten; onCached each live message
	// cached, onChanged each room whose cached messages changed otherwise.
	onRoomsChanged func()
	onCached       func(domain.Message)
	onChanged      func(domain.RoomID)
	// keepDeleted is [display.deleted] keep: an unsend or an edit keeps what it removed.
	keepDeleted atomic.Bool
	// typing is who types in each room, each until they stop or their time passes.
	typingMu sync.Mutex
	typing   map[domain.RoomID]map[string]*time.Timer

	mu       sync.Mutex
	accounts []Account
	// conns are the signed-in accounts' connections, by account name.
	conns   map[string]*conn
	started bool
	stopped bool
	// run is Start's context; an account added later connects under it.
	run context.Context //nolint:containedctx // reloads arrive with no context of their own to outlive

	// txns is each unfinished send's offline threading ID, by its TxnID: a retry goes as
	// the same message (send.go).
	txns map[string]string

	// keeping serializes writing sessions to the keyring: a sign-in's, and a
	// connection's rotated cookies and seq IDs.
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
	return &Adapter{
		cache: cache, secrets: secrets, accounts: slices.Clone(accounts), log: log.With("network", "instagram"),
		said:      map[string]domain.AccountStatus{},
		conns:     map[string]*conn{},
		txns:      map[string]string{},
		typing:    map[domain.RoomID]map[string]*time.Timer{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Instagram.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolInstagram }

// OnStatus sets who hears an account's state change, with a detail for the person
// (empty when none). Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// OnRoomsChanged sets who hears an account's rooms being rewritten in the cache. Set
// before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// OnCached sets who hears each live message cached, and each room whose cached
// messages changed otherwise. Set before Start.
func (a *Adapter) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	a.onCached, a.onChanged = cached, changed
}

func (a *Adapter) roomsChanged() {
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// recount streams a room's locally counted unread state.
func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	if a.cache == nil {
		return
	}
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	emit(a, a.unread, domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: counted})
}

// emit sends v without blocking; a client not reading misses it and catches up from the
// cache, as with the other adapters.
func emit[T any](a *Adapter, ch chan T, v T) {
	a.streamMu.RLock()
	defer a.streamMu.RUnlock()
	if a.closed {
		return
	}
	select {
	case ch <- v:
	default:
	}
}

// zerolog is instameow's logger: warnings and up, whatever kith's level, as messagix's.
func (a *Adapter) zerolog(account string) zerolog.Logger {
	return logging.Zerolog(a.log.With("component", "instameow", "account", account), zerolog.WarnLevel)
}

func (a *Adapter) keepsDeleted() bool { return a.keepDeleted.Load() }

// UseConfig takes [[instagram.account]] and [display.deleted] keep, at start and on
// reload.
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	a.keepDeleted.Store(cfg.Display.Deleted.Keep())
	accounts := make([]Account, 0, len(cfg.Instagram.Accounts))
	for _, account := range cfg.Instagram.Accounts {
		accounts = append(accounts, Account{Name: account.Name})
	}
	a.useAccounts(accounts)
}

// CheckConfig refuses [[instagram.account]]s with a missing or repeated name. It does
// not contact Instagram.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Instagram.Accounts))
	for _, account := range cfg.Instagram.Accounts {
		// Which Instagram account it is comes from signing in, so the name is its only key.
		records = append(records, domain.AccountRecord{Name: account.Name, Key: account.Name})
	}
	return domain.CheckAccounts(cfg.Instagram.Table(), records) //nolint:wrapcheck // names the record itself
}

func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

func (a *Adapter) configured(account string) bool {
	return slices.ContainsFunc(a.accountsNow(), func(acc Account) bool { return acc.Name == account })
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

// SavedSessions is the configured accounts with a kept session. Unusable ones are
// skipped (their state says why); an unreadable keyring is an error.
func (a *Adapter) SavedSessions(context.Context) ([]string, error) {
	var signedIn []string
	for _, account := range a.accountsNow() {
		_, ok, err := loadSession(a.secrets, account.Name)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			signedIn = append(signedIn, account.Name)
		}
	}
	return signedIn, nil
}

// Start connects each signed-in account and runs until ctx ends or Stop is called.
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

// announce connects a signed-in account, or says how to sign it in.
func (a *Adapter) announce(ctx context.Context, account Account) {
	s, ok, err := loadSession(a.secrets, account.Name)
	switch {
	case err != nil:
		a.log.Warn("read the Instagram session failed", "account", account.Name, "err", err)
		a.status(account, SignedOut, "its session could not be read: "+err.Error())
	case !ok:
		a.status(account, SignedOut, "not signed in; run `kith login instagram "+account.Name+"`")
	default:
		a.connect(ctx, account, s)
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
	a.conns = map[string]*conn{}
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

// status says account's state, unless it is what was said last.
func (a *Adapter) status(account Account, phase domain.AccountPhase, detail string) {
	s := domain.AccountStatus{Network: domain.ProtocolInstagram, Account: account.Name, Phase: phase, Detail: detail}
	a.saidMu.Lock()
	same := a.said[account.Name] == s
	a.said[account.Name] = s
	a.saidMu.Unlock()
	if same {
		return
	}
	a.log.Info("account state", "account", account.Name, "phase", phaseNames[phase], "detail", detail)
	if a.onStatus != nil {
		a.onStatus(s)
	}
}

// Messages is the live message stream.
func (a *Adapter) Messages() <-chan domain.Message { return a.messages }

// Activity is typing and receipts.
func (a *Adapter) Activity() <-chan domain.Activity { return a.activity }

// Unread is unread counts as they change.
func (a *Adapter) Unread() <-chan domain.Unread { return a.unread }

// Reactions is reactions as they change.
func (a *Adapter) Reactions() <-chan domain.ReactionUpdate { return a.reactions }

// Me is every Instagram ID that is this person: the connected accounts'.
func (a *Adapter) Me() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	me := make([]string, 0, len(a.conns))
	for _, c := range a.conns {
		me = append(me, person(c.self))
	}
	slices.Sort(me)
	return slices.Compact(me)
}

// person is an Instagram user's ID in kith.
func person(id string) string { return domain.NativePerson(domain.ProtocolInstagram, id) }
