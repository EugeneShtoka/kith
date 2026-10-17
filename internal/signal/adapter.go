// Package signal connects kith to Signal through kith-signal, the helper that alone
// links libsignal (cgo): kithd starts it, keeps it alive with heartbeats, and kills and
// restarts it when it stops answering (supervise.go). Each configured account is one
// Signal account this kith is linked to as a secondary device. The helper keeps only
// the key store; the rest is kithd's.
package signal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each live stream.
const streamBuffer = 64

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("signal: started twice")

// Account is one configured Signal account.
type Account struct {
	// Name is what `kith login signal <name>` takes.
	Name string
}

// A configured account's state.
const (
	// Unlinked has no linked device: never linked, or unlinked from the phone.
	Unlinked = domain.AccountLoggedOut
	// Connecting is linked and waiting for kith-signal.
	Connecting = domain.AccountConnecting
	// Linked is linked, with kith-signal up.
	Linked = domain.AccountOnline
)

// phaseNames are the account states as the log says them.
var phaseNames = map[domain.AccountPhase]string{
	Unlinked: "unlinked", Connecting: "connecting", Linked: "linked", domain.AccountFailed: "failed",
}

// Secrets is where kithd keeps which Signal account each configured one is linked to.
type Secrets interface {
	// Secret is the value under ref; ok is false when there is none.
	Secret(ref string) (value string, ok bool, err error)
	StoreSecret(ref, value string) error
	DeleteSecret(ref string) error
}

// Paths is where the helper's files are.
type Paths struct {
	Store  string // the key store, which only the helper opens
	Socket string // the socket kithd serves the helper through
}

// Adapter is every configured Signal account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	key     []byte // the key store's
	paths   Paths
	log     *slog.Logger

	// onStatus hears an account's state change (see OnStatus); said is each account's
	// last, so a state is said once, not again on every config re-read.
	onStatus func(domain.AccountStatus)
	saidMu   sync.Mutex
	said     map[string]domain.AccountStatus
	// onRoomsChanged hears the account's rooms being rewritten; onCached each live
	// message cached (see OnRoomsChanged, OnCached).
	onRoomsChanged func()
	onCached       func(domain.Message)

	// books is what kithd knows of each account's chats, by its ACI (receive.go).
	booksMu sync.Mutex
	books   map[string]*book

	mu       sync.Mutex
	accounts []Account
	started  bool
	stopped  bool
	// run is Start's context; the helper runs under it.
	run context.Context //nolint:containedctx // reloads arrive with no context of their own to outlive
	// supervisor runs the helper while there is an account (supervise.go).
	supervisor *supervisor
	// helper is the helper while it is up, nil otherwise; up closes when it comes up.
	helper *helper
	up     chan struct{}
	// txns is each unfinished send's sent time, by its TxnID: a retry goes at the same
	// time, so it is the same message (send.go).
	txns map[string]uint64
	// held is events of Signal accounts no configured account is linked to (hold).
	held []*signalv1.EventsResponse
	// down says why the helper is not up, for the accounts' state; failed when it cannot
	// be until something changes (no kith-signal installed).
	down   string
	failed bool

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New is the adapter for accounts, writing into cache, whose key store is at
// paths.Store under key, which secrets maps to their Signal accounts. log may be nil.
func New(cache *db.Cache, secrets Secrets, key []byte, paths Paths, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cache: cache, secrets: secrets, key: key, paths: paths, accounts: slices.Clone(accounts), log: log.With("network", "signal"),
		said:      map[string]domain.AccountStatus{},
		books:     map[string]*book{},
		txns:      map[string]uint64{},
		up:        make(chan struct{}),
		down:      "starting kith-signal",
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// Network is Signal.
func (a *Adapter) Network() domain.Protocol { return domain.ProtocolSignal }

// OnStatus sets who hears an account's state change, with a detail for the person
// (empty when none). Set before Start.
func (a *Adapter) OnStatus(changed func(domain.AccountStatus)) { a.onStatus = changed }

// OnRoomsChanged sets who hears an account's rooms being rewritten in the cache. Set
// before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// OnCached sets who hears each live message cached. Set before Start; changed is never
// called (nothing but a live message changes a room's messages yet).
func (a *Adapter) OnCached(cached func(domain.Message), _ func(domain.RoomID)) { a.onCached = cached }

func (a *Adapter) roomsChanged() {
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// UseConfig takes [[signal.account]], at start and on reload.
func (a *Adapter) UseConfig(_ context.Context, cfg config.Config) {
	accounts := make([]Account, 0, len(cfg.Signal.Accounts))
	for _, account := range cfg.Signal.Accounts {
		accounts = append(accounts, Account{Name: account.Name})
	}
	a.useAccounts(accounts)
}

// CheckConfig refuses [[signal.account]]s with a missing or repeated name. It does not
// contact Signal.
func (a *Adapter) CheckConfig(_ context.Context, cfg config.Config) error {
	records := make([]domain.AccountRecord, 0, len(cfg.Signal.Accounts))
	for _, account := range cfg.Signal.Accounts {
		// Which Signal account it is comes from linking, so the name is its only key.
		records = append(records, domain.AccountRecord{Name: account.Name, Key: account.Name})
	}
	return domain.CheckAccounts(cfg.Signal.Table(), records) //nolint:wrapcheck // names the record itself
}

func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// useAccounts replaces the configured accounts. Once started, the helper runs while
// there is one, and every account's state is said again.
func (a *Adapter) useAccounts(accounts []Account) {
	a.mu.Lock()
	before := a.accounts
	a.accounts = slices.Clone(accounts)
	a.mu.Unlock()
	a.followRename(before, accounts)
	a.superviseNow()
	a.announceAll()
	a.release()
}

// followRename carries an account's link over to its new name: when one linked account
// left the config and one not linked came, in one change, that is a rename (the link is
// kept under the name).
func (a *Adapter) followRename(before, after []Account) {
	gone := slices.DeleteFunc(slices.Clone(before), func(x Account) bool { return slices.Contains(after, x) })
	came := slices.DeleteFunc(slices.Clone(after), func(x Account) bool { return slices.Contains(before, x) })
	if len(gone) != 1 || len(came) != 1 {
		return
	}
	aci, linked, err := a.linkedTo(gone[0].Name)
	if err != nil || !linked {
		return
	}
	if _, taken, err := a.linkedTo(came[0].Name); err != nil || taken {
		return
	}
	if err := a.secrets.StoreSecret(linkRef(came[0].Name), aci); err != nil {
		a.log.Warn("carrying a renamed account's link over failed", "from", gone[0].Name, "to", came[0].Name, "err", err)
		return
	}
	if err := a.secrets.DeleteSecret(linkRef(gone[0].Name)); err != nil {
		a.log.Warn("forgetting a renamed account's old name failed", "name", gone[0].Name, "err", err)
	}
	a.log.Info("renamed: the link goes with it", "from", gone[0].Name, "to", came[0].Name)
}

// SavedSessions is the configured accounts linked to a Signal account. An unreadable
// secret store is an error.
func (a *Adapter) SavedSessions(context.Context) ([]string, error) {
	var linked []string
	for _, account := range a.accountsNow() {
		_, ok, err := a.linkedTo(account.Name)
		if err != nil {
			return nil, err
		}
		if ok {
			linked = append(linked, account.Name)
		}
	}
	return linked, nil
}

// linkRef is where secrets keeps the Signal account (its ACI) an account is linked to.
func linkRef(account string) string { return "signal|" + account }

// linkedTo is the ACI account is linked to; ok is false when it is not.
func (a *Adapter) linkedTo(account string) (aci string, ok bool, err error) {
	aci, ok, err = a.secrets.Secret(linkRef(account))
	if err != nil {
		return "", false, fmt.Errorf("signal: read which Signal account %s is: %w", account, err)
	}
	return aci, ok && aci != "", nil
}

// Start runs the helper while there is an account, and reports each account's state,
// until ctx ends or Stop is called.
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errStartedTwice
	}
	a.started, a.run = true, ctx
	a.mu.Unlock()
	a.superviseNow()
	a.announceAll()
	<-ctx.Done()
	a.stopSupervising()
	return ctx.Err() //nolint:wrapcheck // our own shutdown, as the other adapters'
}

// Stop ends the helper and closes the streams.
func (a *Adapter) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()
	a.stopSupervising()
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.closed = true
	close(a.messages)
	close(a.activity)
	close(a.unread)
	close(a.reactions)
}

// announceAll says every account's state.
func (a *Adapter) announceAll() {
	a.mu.Lock()
	started, h, down, failed := a.started && !a.stopped, a.helper, a.down, a.failed
	a.mu.Unlock()
	if !started {
		return
	}
	for _, account := range a.accountsNow() {
		a.announce(account, h, down, failed)
	}
}

// announce says account's state: whether it is linked, and whether the helper is up.
func (a *Adapter) announce(account Account, h *helper, down string, failed bool) {
	aci, ok, err := a.linkedTo(account.Name)
	switch {
	case err != nil:
		a.status(account, domain.AccountFailed, err.Error())
	case !ok:
		a.status(account, Unlinked, "not linked yet; run `kith login signal "+account.Name+"`")
	case h == nil && failed:
		a.status(account, domain.AccountFailed, down)
	case h == nil:
		a.status(account, Connecting, down)
	case !h.has(aci):
		a.status(account, Unlinked, "unlinked from the phone; run `kith login signal "+account.Name+"` to link again")
	default:
		switch s := h.stateOf(aci); s.state {
		case signalv1.Connection_STATE_CONNECTED:
			a.status(account, Linked, "")
		case signalv1.Connection_STATE_LOGGED_OUT:
			a.status(account, Unlinked, "unlinked from the phone; run `kith login signal "+account.Name+"` to link again")
		case signalv1.Connection_STATE_DISCONNECTED:
			a.status(account, Connecting, "reconnecting to Signal: "+s.detail)
		default:
			a.status(account, Connecting, "connecting to Signal")
		}
	}
}

// status says account's state, unless it is what was said last.
func (a *Adapter) status(account Account, phase domain.AccountPhase, detail string) {
	s := domain.AccountStatus{Network: domain.ProtocolSignal, Account: account.Name, Phase: phase, Detail: detail}
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

// Me is every Signal ID that is this person: the accounts linked, while the helper has
// them.
func (a *Adapter) Me() []string {
	a.mu.Lock()
	h := a.helper
	a.mu.Unlock()
	var me []string
	for _, account := range a.accountsNow() {
		aci, ok, err := a.linkedTo(account.Name)
		if err == nil && ok && h != nil && h.has(aci) {
			me = append(me, domain.NativePerson(domain.ProtocolSignal, aci))
		}
	}
	slices.Sort(me)
	return slices.Compact(me)
}
