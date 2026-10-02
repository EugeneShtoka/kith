package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each stream, as the Matrix adapter's do.
const streamBuffer = 64

// errNotOnWhatsApp is what WhatsApp has no such thing for, or not yet.
var errNotOnWhatsApp = api.ErrNotOnNetwork

// Account is one configured WhatsApp account.
type Account struct {
	// Name is what `kith login whatsapp <name>` takes.
	Name string
	// Digits is the phone number's digits: what pairing takes, and the account part
	// of every room and message ID it sees.
	Digits string
}

// Adapter is every configured WhatsApp account, each a linked device once paired.
// Build it with New.
type Adapter struct {
	cache    *db.Cache
	store    *Store
	accounts []Account
	log      *slog.Logger

	// onRoomsChanged hears an account's rooms being rewritten (see OnRoomsChanged).
	onRoomsChanged func()
	// onCached hears each message cached (see OnCached).
	onCached func(domain.Message)

	mu sync.Mutex
	// clients are the linked accounts' connections, by digits.
	clients map[string]*whatsmeow.Client
	// pairing are the accounts being linked now: one pairing at a time each, or the
	// phone would end up with two kith devices.
	pairing map[string]bool
	// refreshing serializes room refreshes, so an older listing is never written
	// over a newer one.
	refreshing sync.Mutex
	// listing orders a listing's sweep against a message's room: a room a message
	// arrives in is never swept by a listing fetched before it (see heard).
	listing sync.Mutex
	// heard is when each room last got a message, so a listing fetched earlier keeps it.
	heard map[domain.RoomID]time.Time
	// run is Start's context: an account paired later runs until it ends too.
	run     context.Context //nolint:containedctx // events arrive with no context: their work lives as long as Start's
	started bool
	stopped bool
	// sent maps a send's TxnID to the WhatsApp message ID it went out as, so a retry
	// goes out under the same ID and WhatsApp keeps one (see Send).
	sent map[string]types.MessageID

	streamMu  sync.RWMutex
	closed    bool
	messages  chan domain.Message
	activity  chan domain.Activity
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
}

// New is the adapter for accounts over store, writing into cache. log may be nil.
func New(cache *db.Cache, store *Store, accounts []Account, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cache: cache, store: store, accounts: slices.Clone(accounts), log: log.With("network", "whatsapp"),
		clients:   map[string]*whatsmeow.Client{},
		pairing:   map[string]bool{},
		sent:      map[string]types.MessageID{},
		heard:     map[domain.RoomID]time.Time{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// OnRoomsChanged sets who hears an account's rooms being rewritten in the cache. Set
// before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// OnCached sets who hears each message the adapter caches (word completion). Set
// before Start.
func (a *Adapter) OnCached(cached func(domain.Message)) { a.onCached = cached }

// accountsNow is the configured accounts (UseAccounts replaces them).
func (a *Adapter) accountsNow() []Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.accounts)
}

var errStartedTwice = errors.New("whatsapp: Start runs once")

// Start connects every linked account and keeps them connected until ctx ends.
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return errStartedTwice
	}
	a.started, a.run = true, ctx
	a.mu.Unlock()
	for _, account := range a.accountsNow() {
		if err := a.connectLinked(ctx, account); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // our own shutdown, as the Matrix adapter's
}

// connectLinked connects an account if it is linked, and says how to link it if not.
func (a *Adapter) connectLinked(ctx context.Context, account Account) error {
	device, err := a.store.device(ctx, account.Digits)
	if err != nil {
		return err
	}
	if device == nil {
		a.log.Info("not linked yet; run `kith login whatsapp " + account.Name + "`")
		return nil
	}
	a.connect(account, whatsmeow.NewClient(device, newLogger(a.log, account.Name)))
	return nil
}

// connect runs one linked account: its events are handled from now on, and it
// reconnects by itself (whatsmeow does) until Stop.
func (a *Adapter) connect(account Account, client *whatsmeow.Client) {
	a.mu.Lock()
	if a.stopped || a.clients[account.Digits] != nil {
		a.mu.Unlock()
		return
	}
	a.clients[account.Digits] = client
	a.mu.Unlock()
	client.AddEventHandler(func(evt any) { a.handle(account, client, evt) })
	if client.IsConnected() {
		a.refreshLater(account, client)
		return
	}
	if err := client.Connect(); err != nil {
		a.log.Warn("connect failed", "account", account.Name, "err", err)
	}
}

// handle is one account's event.
func (a *Adapter) handle(account Account, client *whatsmeow.Client, evt any) {
	switch e := evt.(type) {
	case *events.Connected, *events.JoinedGroup, *events.GroupInfo:
		a.refreshLater(account, client)
	case *events.Message:
		a.onMessage(a.lifetime(), account, client, e)
	case *events.LoggedOut:
		a.log.Warn("the phone unlinked kith; run `kith login whatsapp "+account.Name+"` again",
			"account", account.Name, "reason", e.Reason.String())
		a.mu.Lock()
		delete(a.clients, account.Digits)
		a.mu.Unlock()
	}
}

// refreshLater rewrites an account's rooms off the event goroutine (whatsmeow handles
// events in order, and a group listing is a round trip).
func (a *Adapter) refreshLater(account Account, client *whatsmeow.Client) {
	ctx := a.lifetime()
	go func() {
		if _, err := a.refreshAccount(ctx, account, client); err != nil && ctx.Err() == nil {
			a.log.Warn("refresh rooms failed", "account", account.Name, "err", err)
		}
	}()
}

// lifetime is Start's context, or a background one before Start.
func (a *Adapter) lifetime() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run != nil {
		return a.run
	}
	return context.Background()
}

// refreshAccount fetches an account's groups and rewrites its rooms and their members.
func (a *Adapter) refreshAccount(ctx context.Context, account Account, client *whatsmeow.Client) ([]domain.Room, error) {
	a.refreshing.Lock()
	defer a.refreshing.Unlock()
	fetched := time.Now()
	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: %s's groups: %w", account.Name, err)
	}
	rooms, members := groupRooms(ctx, account.Digits, groups, a.names(client))
	if err := a.saveListing(ctx, account, rooms, members, fetched); err != nil {
		return nil, err
	}
	return rooms, nil
}

// saveListing writes an account's groups, with their members, over its cached rooms.
// What the sweep must not take is kept in the snapshot: the account's direct chats
// (the listing names groups only), and any room a message arrived in since the
// listing was fetched (a group just joined, a chat just begun) — either would lose
// its history otherwise.
func (a *Adapter) saveListing(ctx context.Context, account Account, rooms []domain.Room, members map[domain.RoomID][]domain.Member, fetched time.Time) error {
	if a.cache == nil {
		return nil
	}
	a.listing.Lock()
	defer a.listing.Unlock()
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	kept, err := a.keptRooms(ctx, owner, rooms, fetched)
	if err != nil {
		return err
	}
	if err := a.cache.SaveRooms(ctx, owner, append(slices.Clone(rooms), kept...)); err != nil {
		return fmt.Errorf("whatsapp: cache %s's rooms: %w", account.Name, err)
	}
	for id, list := range members {
		if err := a.cache.SaveMembers(ctx, id, list); err != nil {
			return fmt.Errorf("whatsapp: cache members of %s: %w", id, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}

// keptRooms is an owner's cached rooms a listing must not sweep: its direct chats, and
// rooms heard from since the listing was fetched. Caller holds listing.
func (a *Adapter) keptRooms(ctx context.Context, owner domain.RoomOwner, listed []domain.Room, fetched time.Time) ([]domain.Room, error) {
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		if !owner.Owns(r.ID) || slices.ContainsFunc(listed, func(l domain.Room) bool { return l.ID == r.ID }) {
			return true
		}
		return !isDirect(r.ID) && a.heard[r.ID].Before(fetched)
	}), nil
}

// isDirect reports whether a WhatsApp room is a chat with one person, by its JID.
func isDirect(roomID domain.RoomID) bool {
	jid, err := types.ParseJID(domain.ParseID(string(roomID)).Native)
	return err == nil && (jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer)
}

// names looks people up in an account's contacts.
func (a *Adapter) names(client *whatsmeow.Client) nameOf {
	return func(ctx context.Context, person types.JID) string {
		contact, err := client.Store.Contacts.GetContact(ctx, person)
		if err != nil {
			a.log.Debug("read a contact failed", "err", err)
			return ""
		}
		return contactName(contact)
	}
}

// connected is every account with a live client, in config order.
func (a *Adapter) connected() []struct {
	account Account
	client  *whatsmeow.Client
} {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []struct {
		account Account
		client  *whatsmeow.Client
	}
	for i := range a.accounts {
		account := a.accounts[i]
		if client := a.clients[account.Digits]; client != nil {
			out = append(out, struct {
				account Account
				client  *whatsmeow.Client
			}{account, client})
		}
	}
	return out
}

// Stop disconnects every account and ends the streams.
func (a *Adapter) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	clients := a.clients
	a.clients = map[string]*whatsmeow.Client{}
	a.mu.Unlock()
	for _, client := range clients {
		client.Disconnect()
	}
	// Under the lock emit sends under, so a late event never sends on a closed one.
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.closed = true
	close(a.messages)
	close(a.activity)
	close(a.unread)
	close(a.reactions)
}

// emit delivers v unless the streams are closed. Lossy on a full buffer, as Matrix's:
// what it carries is in the cache too.
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

func (a *Adapter) Messages() <-chan domain.Message         { return a.messages }
func (a *Adapter) Activity() <-chan domain.Activity        { return a.activity }
func (a *Adapter) Unread() <-chan domain.Unread            { return a.unread }
func (a *Adapter) Reactions() <-chan domain.ReactionUpdate { return a.reactions }

// Account is empty: the daemon is named after the Matrix account.
func (a *Adapter) Account() string { return "" }

// Me is every linked account's own IDs: its phone number and its LID.
func (a *Adapter) Me() []string {
	var me []string
	for _, c := range a.connected() {
		own := selfOf(c.client)
		for _, jid := range []types.JID{own.pn, own.lid} {
			if !jid.IsEmpty() {
				me = append(me, domain.NativePerson(domain.ProtocolWhatsApp, jid.String()))
			}
		}
	}
	return me
}

// selfOf is a linked client's own addresses.
func selfOf(client *whatsmeow.Client) self {
	var own self
	if id := client.Store.ID; id != nil {
		own.pn = id.ToNonAD()
	}
	if lid := client.Store.GetLID(); !lid.IsEmpty() {
		own.lid = lid.ToNonAD()
	}
	return own
}

// RewindSync has nothing to rewind: WhatsApp sends history once, when linking.
func (a *Adapter) RewindSync(context.Context) error { return nil }

// Rooms is every WhatsApp room in the cache, whichever account sees it.
func (a *Adapter) Rooms(ctx context.Context) ([]domain.Room, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read cached rooms: %w", err)
	}
	return slices.DeleteFunc(rooms, func(r domain.Room) bool {
		return domain.NetworkOf(string(r.ID)) != domain.ProtocolWhatsApp
	}), nil
}

// RefreshRooms refetches every linked account's groups.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	var out []domain.Room
	for _, c := range a.connected() {
		rooms, err := a.refreshAccount(ctx, c.account, c.client)
		if err != nil {
			return nil, err
		}
		out = append(out, rooms...)
	}
	return out, nil
}

// CachedUnread is nothing yet: WhatsApp's unread state comes with its messages.
func (a *Adapter) CachedUnread(context.Context) ([]domain.Unread, error) { return nil, nil }

// CanonicalParent: WhatsApp rooms sit in no space.
func (a *Adapter) CanonicalParent(context.Context, domain.RoomID) (domain.SpaceID, error) {
	return "", nil
}

// StarMessage bookmarks a message; WhatsApp's star is not synced, so it is kith's own.
func (a *Adapter) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, starred bool) error {
	if a.cache == nil {
		return nil
	}
	set, err := a.cache.Starred(ctx, roomID)
	if err != nil {
		return fmt.Errorf("whatsapp: read stars in %s: %w", roomID, err)
	}
	set = slices.DeleteFunc(set, func(id domain.EventID) bool { return id == eventID })
	if starred {
		set = append(set, eventID)
	}
	if err := a.cache.SetStarred(ctx, roomID, set, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("whatsapp: save stars in %s: %w", roomID, err)
	}
	return nil
}

// MarkSpam records a verdict kith keeps itself (WhatsApp has nowhere to put one).
func (a *Adapter) MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error {
	if a.cache == nil {
		return nil
	}
	var err error
	if !verdict.Spam() && !verdict.Released {
		err = a.cache.ClearSpam(ctx, verdict.Room)
	} else {
		err = a.cache.SetSpam(ctx, verdict)
	}
	if err != nil {
		return fmt.Errorf("whatsapp: record the spam verdict on %s: %w", verdict.Room, err)
	}
	return nil
}

// Members is a room's cached members.
func (a *Adapter) Members(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	if a.cache == nil {
		return nil, nil
	}
	members, err := a.cache.Members(ctx, roomID, limit)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read members of %s: %w", roomID, err)
	}
	return members, nil
}

// RefreshMembers refetches the account's groups (a group's members come with them).
func (a *Adapter) RefreshMembers(ctx context.Context, roomID domain.RoomID) ([]domain.Member, error) {
	account := domain.ParseID(string(roomID)).Account
	for _, c := range a.connected() {
		if c.account.Digits == account {
			if _, err := a.refreshAccount(ctx, c.account, c.client); err != nil {
				return nil, err
			}
		}
	}
	return a.Members(ctx, roomID, 0)
}

// MentionCandidates orders a room's members for the mention dropdown, as Matrix's
// are: recent speakers, the most mentioned, then everyone alphabetically.
func (a *Adapter) MentionCandidates(ctx context.Context, roomID domain.RoomID, limit int) ([]domain.Member, error) {
	members, err := a.Members(ctx, roomID, 0)
	if err != nil || len(members) == 0 || a.cache == nil {
		return domain.RankMembers(members, nil, nil, limit), err
	}
	speakers, err := a.cache.RecentSpeakers(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: rank recent speakers: %w", err)
	}
	mentioned, err := a.cache.FrequentMentions(ctx, roomID, domain.MentionRankRung)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: rank mention history: %w", err)
	}
	return domain.RankMembers(members, speakers, mentioned, limit), nil
}

// DirectCandidates is nobody yet: DMs come with messages.
func (a *Adapter) DirectCandidates(context.Context, int) ([]domain.Member, error) { return nil, nil }

// RoomEncryption: every WhatsApp chat is end-to-end encrypted.
func (a *Adapter) RoomEncryption(_ context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error) {
	out := make(map[domain.RoomID]bool, len(roomIDs))
	for _, room := range roomIDs {
		out[room] = true
	}
	return out, nil
}

// What comes with messages (4b) is refused until then.

func (a *Adapter) MarkRead(context.Context, domain.RoomID, domain.EventID, bool) error {
	return errNotYet("send read receipts")
}

func (a *Adapter) MarkRoomsRead(context.Context, []domain.RoomID, bool) (domain.ReadResult, error) {
	return domain.ReadResult{}, errNotYet("send read receipts")
}

func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error {
	return errNotYet("mark a chat unread")
}

func (a *Adapter) MessageHistory(context.Context, domain.RoomID, domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	return nil, domain.Deletion{}, errNotYet("show a message's history")
}

// Timeline is empty: WhatsApp keeps no history to page through; what kith has is in
// the cache.
func (a *Adapter) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}

func (a *Adapter) FetchEvent(context.Context, domain.RoomID, domain.EventID) (domain.Message, error) {
	return domain.Message{}, errNotYet("fetch a message")
}

func (a *Adapter) Redact(context.Context, domain.RoomID, domain.EventID, string) error {
	return errNotYet("delete messages")
}

func (a *Adapter) SendTyping(context.Context, domain.RoomID, bool, time.Duration) error { return nil }

func (a *Adapter) SendFile(context.Context, domain.RoomID, string, string) error {
	return errNotYet("send files")
}

func (a *Adapter) SendReaction(context.Context, domain.RoomID, domain.EventID, string) error {
	return errNotYet("react")
}

func (a *Adapter) LoadImage(context.Context, domain.RoomID, domain.EventID) ([]byte, error) {
	return nil, errNotYet("load media")
}
