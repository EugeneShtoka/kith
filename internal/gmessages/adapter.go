// Package gmessages connects kith to Google Messages, paired with the Messages app on
// an Android phone as Messages for web is, over mautrix-gmessages' libgm in-process (no
// bridge). Each configured account is one paired phone. It writes into the shared
// cache, so search, drafts, the assistant and notifications work as for Matrix.
package gmessages

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each live stream.
const streamBuffer = 64

// phaseNames are the account states as the log says them.
var phaseNames = map[domain.AccountPhase]string{
	SignedOut: "not paired", Connecting: "connecting", Connected: "connected", domain.AccountFailed: "failed",
}

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("gmessages: started twice")

// Account is one configured Google Messages account: a paired phone.
type Account struct {
	// Name is what `kith login gmessages <name>` takes.
	Name string
}

// A configured account's state.
const (
	// SignedOut has no pairing: never paired, or unpaired from the phone.
	SignedOut = domain.AccountLoggedOut
	// Connecting is paired and reaching the phone.
	Connecting = domain.AccountConnecting
	// Connected is paired and connected.
	Connected = domain.AccountOnline
)

// Adapter is every configured Google Messages account. Build it with New.
type Adapter struct {
	secrets Secrets
	log     *slog.Logger

	// onStatus hears an account's state change (see OnStatus).
	onStatus func(domain.AccountStatus)

	mu       sync.Mutex
	accounts []Account
	// conns are the paired accounts' connections, by account name.
	conns map[string]*conn
	// signIns counts each account's pairings, so a connection begun on an older one
	// never replaces a newer one (see adopt).
	signIns map[string]int
	started bool
	stopped bool
	// run is Start's context; an account added later connects under it.
	run context.Context //nolint:containedctx // reloads arrive with no context of their own to outlive

	// keeping serializes writing sessions to the secret store: a pairing's and a
	// connection's refreshed token, so an older session never overwrites a newer one.
	keeping sync.Mutex

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New is the adapter for accounts, their sessions in secrets. log may be nil.
func New(secrets Secrets, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		secrets: secrets, accounts: slices.Clone(accounts), log: log.With("network", "gmessages"),
		conns:     map[string]*conn{},
		signIns:   map[string]int{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Google Messages.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolGMessages }

// OnStatus sets who hears an account's state change, with a detail for the person
// (empty when none). Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// UseConfig takes [[gmessages.account]], at start and on reload.
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	accounts := make([]Account, 0, len(cfg.GMessages.Accounts))
	for _, account := range cfg.GMessages.Accounts {
		accounts = append(accounts, Account{Name: account.Name})
	}
	a.useAccounts(accounts)
}

// CheckConfig refuses [[gmessages.account]]s with a missing or repeated name. It does
// not contact Google.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.GMessages.Accounts))
	for _, account := range cfg.GMessages.Accounts {
		// Which phone it is comes from pairing, so the name is its only key.
		records = append(records, domain.AccountRecord{Name: account.Name, Key: account.Name})
	}
	return domain.CheckAccounts(cfg.GMessages.Table(), records) //nolint:wrapcheck // names the record itself
}

func (a *Adapter) session(account Account, phase domain.AccountPhase, detail string) {
	a.log.Info("account state", "account", account.Name, "phase", phaseNames[phase], "detail", detail)
	if a.onStatus != nil {
		a.onStatus(domain.AccountStatus{Network: domain.ProtocolGMessages, Account: account.Name, Phase: phase, Detail: detail})
	}
}

func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// useAccounts replaces the configured accounts; once started, an added paired account
// connects and a removed one is let go.
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

// SavedSessions is the configured accounts with a stored pairing. Unusable ones are
// skipped (their status says why); an unreadable store is an error.
func (a *Adapter) SavedSessions(context.Context) ([]string, error) {
	var paired []string
	for _, account := range a.accountsNow() {
		_, ok, err := loadSession(a.secrets, account.Name)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			paired = append(paired, account.Name)
		}
	}
	return paired, nil
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

// announce connects a paired account, or reports how to pair it.
func (a *Adapter) announce(ctx context.Context, account Account) {
	s, ok, err := loadSession(a.secrets, account.Name)
	switch {
	case err != nil:
		a.log.Warn("read the Google Messages session failed", "account", account.Name, "err", err)
		a.session(account, SignedOut, "its pairing could not be read: "+err.Error())
	case !ok:
		hint := "not paired; run `kith login gmessages " + account.Name + "`"
		a.log.Info(hint, "account", account.Name)
		a.session(account, SignedOut, hint)
	default:
		a.log.Info("paired", "account", account.Name, "phone", s.PhoneID)
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

// Me is nobody yet: who you are in conversations (your number) comes with them.
func (a *Adapter) Me() []string { return nil }
