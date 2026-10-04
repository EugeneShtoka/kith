// Package slack is kith's own link to Slack: each configured account is one workspace
// it is signed in to, as the Slack web client is. It writes what it sees into the
// cache every network shares, so search, drafts, the assistant and notifications work
// for it as they do for Matrix.
package slack

import (
	"context"
	"errors"
	"fmt"
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
var errStartedTwice = errors.New("slack: started twice")

// Account is one configured Slack account: a workspace.
type Account struct {
	// Name is what `kith login slack <name>` takes.
	Name string
	// Workspace is its address, "acme" for acme.slack.com.
	Workspace string
}

// Session is a configured account's state.
type Session int

const (
	// SignedOut has no credentials: never signed in, or signed out by Slack.
	SignedOut Session = iota + 1
	// Connecting is signed in and reaching Slack.
	Connecting
	// Connected is signed in and connected.
	Connected
)

// Adapter is every configured Slack account. Build it with New.
type Adapter struct {
	cache   *db.Cache
	secrets Secrets
	log     *slog.Logger

	// onSession hears an account's state change (see OnSession).
	onSession func(Account, Session, string)
	// onRoomsChanged hears an account's rooms being rewritten (see OnRoomsChanged).
	onRoomsChanged func()
	// onCached hears each message cached, onChanged each room whose cached messages
	// changed otherwise (see OnCached).
	onCached  func(domain.Message)
	onChanged func(domain.RoomID)

	mu       sync.Mutex
	accounts []Account
	// workspaces are the signed-in accounts' connections, by account name.
	workspaces map[string]*workspace
	// signIns counts each account's sign-ins, so a connection begun on an older
	// session never replaces a newer one (see adopt).
	signIns map[string]int
	started bool
	stopped bool
	// run is Start's context: an account added later is connected for as long.
	run context.Context //nolint:containedctx // reloads arrive with no context of their own to outlive

	// refreshing serializes listings, so an older one is never written over a newer.
	refreshing sync.Mutex
	// listing is held while a listing is written and while a message is, so the two
	// never interleave; heard is when each room last had a message cached (see
	// keptRooms).
	listing sync.Mutex
	heard   map[domain.RoomID]time.Time

	// keepDeleted is [display.deleted] keep (KeepDeleted). positions are the rooms'
	// read positions, loaded on first use (unread.go); typing is who is typing where,
	// each forgotten when their timer fires (typing.go); reacted is when each message's
	// reactions last changed live (reactions.go). All under mu.
	keepDeleted bool
	positions   map[domain.RoomID]time.Time
	typing      map[domain.RoomID]map[string]*time.Timer
	reacted     map[domain.EventID]time.Time
	// threadsAsked is the newest reply each thread was last read up to, or queued to
	// be; threadQueue is the threads waiting to be read, readingThreads whether they
	// are being (threads.go). Under mu.
	threadsAsked   map[domain.EventID]string
	threadQueue    []threadWant
	readingThreads bool
	// repliesCached is when each thread reply was cached, lately (threads.go). Under
	// mu.
	repliesCached map[domain.EventID]time.Time

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
		cache: cache, secrets: secrets, accounts: slices.Clone(accounts), log: log.With("network", "slack"),
		workspaces:    map[string]*workspace{},
		signIns:       map[string]int{},
		heard:         map[domain.RoomID]time.Time{},
		typing:        map[domain.RoomID]map[string]*time.Timer{},
		reacted:       map[domain.EventID]time.Time{},
		threadsAsked:  map[domain.EventID]string{},
		repliesCached: map[domain.EventID]time.Time{},
		messages:      make(chan domain.Message, streamBuffer),
		activity:      make(chan domain.Activity, streamBuffer),
		unread:        make(chan domain.Unread, streamBuffer),
		reactions:     make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// OnSession sets who hears an account's state change, with what to tell a person
// about it (empty when nothing). Set before Start.
func (a *Adapter) OnSession(changed func(account Account, session Session, detail string)) {
	a.onSession = changed
}

// OnRoomsChanged sets who hears an account's rooms being rewritten in the cache. Set
// before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// OnCached sets who hears each message the adapter caches, and each room whose cached
// messages changed otherwise (word completion). Set before Start.
func (a *Adapter) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	a.onCached, a.onChanged = cached, changed
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
// added is connected if signed in, as one configured at Start is; one removed is let
// go.
func (a *Adapter) UseAccounts(accounts []Account) {
	a.mu.Lock()
	known := a.accounts
	a.accounts = slices.Clone(accounts)
	for name, w := range a.workspaces {
		if !slices.ContainsFunc(accounts, func(k Account) bool { return k.Name == name }) {
			w.close()
			delete(a.workspaces, name)
		}
	}
	started, ctx := a.started && !a.stopped, a.run
	a.mu.Unlock()
	if !started {
		return
	}
	for _, account := range accounts {
		if !slices.ContainsFunc(known, func(k Account) bool { return k.Name == account.Name }) {
			a.announce(ctx, account)
		}
	}
}

// SignedIn is the configured accounts that have credentials kept. An account whose
// credentials are unusable is not one (its status says why); a store that cannot be
// read is an error.
func (a *Adapter) SignedIn() ([]Account, error) {
	var signed []Account
	for _, account := range a.accountsNow() {
		_, ok, err := loadCredentials(a.secrets, account.Name)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			signed = append(signed, account)
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

// announce connects an account that is signed in, and says how to sign in when it is
// not.
func (a *Adapter) announce(ctx context.Context, account Account) {
	creds, ok, err := loadCredentials(a.secrets, account.Name)
	switch {
	case err != nil:
		a.log.Warn("read the Slack credentials failed", "account", account.Name, "err", err)
		a.session(account, SignedOut, "its credentials could not be read: "+err.Error())
	case !ok:
		hint := "not signed in; run `kith login slack " + account.Name + "`"
		a.log.Info(hint, "account", account.Name)
		a.session(account, SignedOut, hint)
	default:
		a.log.Info("signed in", "account", account.Name, "team", creds.Team, "user", creds.User)
		go a.connect(ctx, account, creds)
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
	for _, w := range a.workspaces {
		w.close()
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

// Me is every Slack ID that is this person: one per connected workspace.
func (a *Adapter) Me() []string {
	var me []string
	for _, w := range a.connected() {
		me = append(me, personID(w.creds.Team, w.creds.User))
	}
	slices.Sort(me)
	return me
}

// RewindSync has nothing to refill before a workspace is connected.
func (a *Adapter) RewindSync(context.Context) error { return nil }

// Rooms is the Slack rooms in the cache.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolSlack
	}), nil
}

// RefreshRooms lists every connected workspace's conversations again.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	var out []domain.Room
	for _, w := range a.connected() {
		rooms, err := a.list(ctx, w)
		if err != nil {
			return nil, err
		}
		out = append(out, rooms...)
	}
	return out, nil
}

// Spaces is the workspaces, each the space of its rooms, from the cache.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) {
	if a.cache == nil {
		return nil, nil
	}
	spaces, err := a.cache.Spaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack: read cached workspaces: %w", err)
	}
	spaces = slices.DeleteFunc(spaces, func(s domain.Space) bool {
		return domain.NetworkOf(string(s.ID)) != domain.ProtocolSlack
	})
	// Not stored (Matrix derives it on read too): a workspace is its rooms' home.
	for i := range spaces {
		spaces[i].Original = true
	}
	return spaces, nil
}

// RefreshSpaces lists the workspaces again (one listing writes rooms and space), then
// answers from the cache.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	if _, err := a.RefreshRooms(ctx); err != nil {
		return nil, err
	}
	return a.Spaces(ctx)
}

// errNoMarkUnread is marking a Slack conversation unread, which kith does not do yet.
var errNoMarkUnread = errors.New("slack: marking a conversation unread is not supported yet")

// MarkRoomUnread is not supported yet.
func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error { return errNoMarkUnread }

// StarMessage needs a connected workspace.
func (a *Adapter) StarMessage(context.Context, domain.RoomID, domain.EventID, bool) error {
	return errNetworkOff
}

// MarkSpam needs a connected workspace.
func (a *Adapter) MarkSpam(context.Context, domain.SpamVerdict) error { return errNetworkOff }

// CanonicalParent is a Slack room's workspace.
func (a *Adapter) CanonicalParent(_ context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	id := domain.ParseID(string(roomID))
	if id.Network != domain.ProtocolSlack || id.Account == "" {
		return "", nil
	}
	return workspaceSpaceID(id.Account), nil
}

// SendFile needs a connected workspace.
func (a *Adapter) SendFile(context.Context, domain.RoomID, string, string) error {
	return errNetworkOff
}

// LoadImage needs a connected workspace.
func (a *Adapter) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return nil, errNetworkOff
}

// DirectCandidates is who a DM may be started with: none before a workspace is
// connected.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption: Slack rooms are not end-to-end encrypted.
func (a *Adapter) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, id := range roomIDs {
		out[id] = false
	}
	return out, nil
}
