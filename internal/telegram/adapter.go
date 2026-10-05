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
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each stream, as the other adapters' do.
const streamBuffer = 64

// errNetworkOff is an account that is not signed in or not connected.
var errNetworkOff = api.ErrNetworkOff

// errStartedTwice guards Start, which runs once.
var errStartedTwice = errors.New("telegram: started twice")

// Account is one configured Telegram account: a phone number.
type Account struct {
	// Name is what `kith login telegram <name>` takes.
	Name string
	// Digits is its phone number's digits, as Telegram takes it.
	Digits string
}

// Session is a configured account's state.
type Session int

const (
	// LoggedOut has no credentials: never logged in, or logged out by Telegram.
	LoggedOut Session = iota + 1
	// Connecting is logged in and reaching Telegram.
	Connecting
	// Connected is logged in and connected.
	Connected
)

// Adapter is every configured Telegram account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	log     *slog.Logger

	// onSession hears an account's state change (see OnSession).
	onSession func(Account, Session, string)

	mu       sync.Mutex
	accounts []Account
	started  bool
	stopped  bool

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
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// OnSession sets who hears an account's state change, with what to tell a person
// about it (empty when nothing). Set before Start.
func (a *Adapter) OnSession(changed func(account Account, session Session, detail string)) {
	a.onSession = changed
}

// session reports an account's state.
func (a *Adapter) session(account Account, s Session, detail string) {
	if a.onSession != nil {
		a.onSession(account, s, detail)
	}
}

// accountsNow is the configured accounts (UseAccounts replaces them).
func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

// UseAccounts takes the configured accounts after the config is re-read: an account
// added says whether it is logged in, as one configured at Start does.
func (a *Adapter) UseAccounts(accounts []Account) {
	a.mu.Lock()
	known := a.accounts
	a.accounts = slices.Clone(accounts)
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
	a.started = true
	a.mu.Unlock()
	for _, account := range a.accountsNow() {
		a.announce(account)
	}
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // our own shutdown, as the other adapters'
}

// announce says whether an account is logged in, and how to log in when it is not.
func (a *Adapter) announce(account Account) {
	creds, ok, err := loadCredentials(a.secrets, account.Digits)
	switch {
	case err != nil:
		a.log.Warn("read the Telegram credentials failed", "account", account.Name, "err", err)
		a.session(account, LoggedOut, "its credentials could not be read: "+err.Error())
	case !ok:
		hint := "not logged in; :login telegram in kith, or `kith login telegram " + account.Name + "`"
		a.log.Info(hint, "account", account.Name)
		a.session(account, LoggedOut, hint)
	default:
		a.log.Info("logged in", "account", account.Name, "user", creds.User)
		a.session(account, Connecting, "")
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

// Me is every Telegram ID that is this person: none until an account is connected.
func (a *Adapter) Me() []string { return nil }

// RewindSync has nothing to refill before an account is connected.
func (a *Adapter) RewindSync(context.Context) error { return nil }

// Rooms is the Telegram rooms in the cache: none before an account is connected.
func (a *Adapter) Rooms(context.Context) ([]domain.Room, error) { return nil, nil }

// RefreshRooms lists the accounts' rooms: none before one is connected.
func (a *Adapter) RefreshRooms(context.Context) ([]domain.Room, error) { return nil, nil }

// CachedUnread is the Telegram rooms' unread counts: none before an account is connected.
func (a *Adapter) CachedUnread(context.Context) ([]domain.Unread, error) { return nil, nil }

// MarkRead needs a connected account.
func (a *Adapter) MarkRead(context.Context, domain.RoomID, domain.EventID, bool) error {
	return errNetworkOff
}

// MarkRoomsRead needs a connected account.
func (a *Adapter) MarkRoomsRead(context.Context, []domain.RoomID, bool) (domain.ReadResult, error) {
	return domain.ReadResult{}, errNetworkOff
}

// MarkRoomUnread needs a connected account.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNetworkOff }

// StarMessage needs a connected account.
func (a *Adapter) StarMessage(context.Context, domain.RoomID, domain.EventID, bool) error {
	return errNetworkOff
}

// MarkSpam needs a connected account.
func (a *Adapter) MarkSpam(context.Context, domain.SpamVerdict) error { return errNetworkOff }

// CanonicalParent: a Telegram room's home is its account's space, from the listing on.
func (a *Adapter) CanonicalParent(context.Context, domain.RoomID) (domain.SpaceID, error) {
	return "", nil
}

// MessageHistory needs a connected account.
func (a *Adapter) MessageHistory(context.Context, domain.RoomID, domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	return nil, domain.Deletion{}, errNetworkOff
}

// Timeline needs a connected account.
func (a *Adapter) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, errNetworkOff
}

// FetchEvent needs a connected account.
func (a *Adapter) FetchEvent(context.Context, domain.RoomID, domain.EventID) (domain.Message, error) {
	return domain.Message{}, errNetworkOff
}

// Redact needs a connected account.
func (a *Adapter) Redact(context.Context, domain.RoomID, domain.EventID, string) error {
	return errNetworkOff
}

// Send needs a connected account.
func (a *Adapter) Send(context.Context, domain.RoomID, domain.Draft) error { return errNetworkOff }

// SendTyping needs a connected account.
func (a *Adapter) SendTyping(context.Context, domain.RoomID, bool, time.Duration) error {
	return errNetworkOff
}

// SendFile needs a connected account.
func (a *Adapter) SendFile(context.Context, domain.RoomID, string, string) error {
	return errNetworkOff
}

// SendReaction needs a connected account.
func (a *Adapter) SendReaction(context.Context, domain.RoomID, domain.EventID, string) error {
	return errNetworkOff
}

// LoadImage needs a connected account.
func (a *Adapter) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return nil, errNetworkOff
}

// Members is a room's members: none before an account is connected.
func (a *Adapter) Members(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return nil, nil
}

// RefreshMembers needs a connected account.
func (a *Adapter) RefreshMembers(context.Context, domain.RoomID) ([]domain.Member, error) {
	return nil, errNetworkOff
}

// MentionCandidates is who may be mentioned: none before an account is connected.
func (a *Adapter) MentionCandidates(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return nil, nil
}

// DirectCandidates is who a DM may be started with: none before an account is
// connected.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption: Telegram rooms are not end-to-end encrypted.
func (a *Adapter) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, id := range roomIDs {
		out[id] = false
	}
	return out, nil
}
