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

// errNetworkOff is an account that is not linked or not connected.
var errNetworkOff = api.ErrNetworkOff

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
	// onLink hears an account's connection change (see OnLink).
	onLink func(Account, Link, string)
	// onCached hears each message cached, onChanged each room whose cached messages
	// changed otherwise (a history chunk) — see OnCached.
	onCached  func(domain.Message)
	onChanged func(domain.RoomID)

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
	// positions are the WhatsApp rooms' read positions, by time; nil until first needed.
	positions map[domain.RoomID]time.Time
	// typing is who is typing in each room now (see typing.go).
	typing map[domain.RoomID][]string
	// channels is what each account's last listing said of the channels it follows
	// (see channels.go).
	channels map[domain.RoomID]channel
	// listedAt is when each account was last listed, and trailing marks an account
	// with a listing waiting for listingEvery to pass (see refreshLater).
	listedAt map[string]time.Time
	trailing map[string]bool
	// keepDeleted is [display.deleted] keep (see KeepDeleted).
	keepDeleted bool
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
		typing:    map[domain.RoomID][]string{},
		channels:  map[domain.RoomID]channel{},
		listedAt:  map[string]time.Time{},
		trailing:  map[string]bool{},
		messages:  make(chan domain.Message, streamBuffer),
		activity:  make(chan domain.Activity, streamBuffer),
		unread:    make(chan domain.Unread, streamBuffer),
		reactions: make(chan domain.ReactionUpdate, streamBuffer),
	}
}

// OnRoomsChanged sets who hears an account's rooms being rewritten in the cache. Set
// before Start.
func (a *Adapter) OnRoomsChanged(changed func()) { a.onRoomsChanged = changed }

// Link is a configured account's connection.
type Link int

const (
	// Unlinked has no linked device: never paired, or unlinked by the phone.
	Unlinked Link = iota + 1
	// Connecting is linked and reaching WhatsApp.
	Connecting
	// Connected is linked and connected.
	Connected
)

// OnLink sets who hears an account's connection change, with what to tell a person
// about it (empty when nothing). Set before Start.
func (a *Adapter) OnLink(changed func(account Account, link Link, detail string)) { a.onLink = changed }

// link tells the listener an account's connection changed.
func (a *Adapter) link(account Account, link Link, detail string) {
	if a.onLink != nil {
		a.onLink(account, link, detail)
	}
}

// Linked is every configured account that has a linked device: those Start connects.
func (a *Adapter) Linked(ctx context.Context) ([]Account, error) {
	var linked []Account
	for _, account := range a.accountsNow() {
		device, err := a.store.device(ctx, account.Digits)
		if err != nil {
			return nil, err
		}
		if device != nil {
			linked = append(linked, account)
		}
	}
	return linked, nil
}

// OnCached sets who hears each message the adapter caches, and each room whose
// cached messages changed otherwise (word completion). Set before Start.
func (a *Adapter) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	a.onCached, a.onChanged = cached, changed
}

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
		hint := "not linked yet; run `kith login whatsapp " + account.Name + "`"
		a.log.Info(hint, "account", account.Name)
		a.link(account, Unlinked, hint)
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
		// Just paired: its Connected event came before this handler.
		a.link(account, Connected, "")
		a.refreshLater(account, client)
		return
	}
	a.link(account, Connecting, "")
	if err := client.Connect(); err != nil {
		a.log.Warn("connect failed", "account", account.Name, "err", err)
		a.link(account, Connecting, "connect failed: "+err.Error())
	}
}

// handle is one account's event.
func (a *Adapter) handle(account Account, client *whatsmeow.Client, evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		a.link(account, Connected, "")
		a.refreshLater(account, client)
	case *events.JoinedGroup, *events.GroupInfo:
		a.refreshLater(account, client)
	case *events.Disconnected:
		// Only a client still in use reconnects: one unlinked or dropped from the
		// config is gone, and says so already.
		a.mu.Lock()
		current := a.clients[account.Digits] == client
		a.mu.Unlock()
		if current {
			a.link(account, Connecting, "disconnected; reconnecting")
		}
	case *events.Message:
		a.onMessage(a.lifetime(), account, client, e)
	case *events.HistorySync:
		a.onHistory(a.lifetime(), account, client, e)
	case *events.Receipt:
		a.onReceipt(a.lifetime(), account, e)
	case *events.ChatPresence:
		a.onTyping(a.lifetime(), account, client, e)
	case *events.LoggedOut:
		a.log.Warn("the phone unlinked kith; run `kith login whatsapp "+account.Name+"` again",
			"account", account.Name, "reason", e.Reason.String())
		a.mu.Lock()
		delete(a.clients, account.Digits)
		a.mu.Unlock()
		a.link(account, Unlinked, "the phone unlinked kith; run `kith login whatsapp "+account.Name+"` again")
	}
}

// refreshLater rewrites an account's rooms off the event goroutine (whatsmeow handles
// events in order, and a group listing is a round trip). An account listed moments
// ago is listed once more when listingEvery has passed, however many changes arrive
// meanwhile: WhatsApp refuses listings that come faster.
func (a *Adapter) refreshLater(account Account, client *whatsmeow.Client) {
	ctx := a.lifetime()
	wait := a.untilListable(account.Digits)
	if wait > 0 {
		a.mu.Lock()
		pending := a.trailing[account.Digits]
		a.trailing[account.Digits] = true
		a.mu.Unlock()
		if pending {
			return // the listing already waiting covers this change
		}
	}
	go func() {
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			a.mu.Lock()
			delete(a.trailing, account.Digits)
			a.mu.Unlock()
		}
		if _, err := a.refreshAccount(ctx, account, client, false); err != nil && ctx.Err() == nil {
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

// refreshAccount fetches an account's groups and channels and rewrites its rooms and
// their members. ifStale answers from the cache instead when the account was listed
// less than listingEvery ago (a client's refresh; an event's always lists).
func (a *Adapter) refreshAccount(ctx context.Context, account Account, client *whatsmeow.Client, ifStale bool) ([]domain.Room, error) {
	a.refreshing.Lock()
	defer a.refreshing.Unlock()
	if ifStale && a.untilListable(account.Digits) > 0 {
		return a.accountRooms(ctx, account, func(domain.RoomID) bool { return true })
	}
	fetched := time.Now()
	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: %s's groups: %w", account.Name, err)
	}
	spaces, chats := communities(ctx, account.Digits, groups, func(ctx context.Context, jid types.JID) string {
		info, gerr := client.GetGroupInfo(ctx, jid)
		if gerr != nil {
			a.log.Debug("read a community's name failed", "account", account.Name, "err", gerr)
			return ""
		}
		return info.Name
	})
	rooms, members := groupRooms(ctx, account.Digits, chats, a.names(client))
	// A failed channel listing fails the refresh: written without them, the sweep
	// would take the channels' history.
	channelList, err := a.listChannels(ctx, account, client)
	if err != nil {
		return nil, err
	}
	rooms = append(rooms, channelList...)
	domain.SortRooms(rooms)
	if err := a.saveListing(ctx, account, groupListing{rooms: rooms, members: members, spaces: spaces}, fetched); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.listedAt[account.Digits] = time.Now()
	a.mu.Unlock()
	a.fetchChannelHistory(ctx, account, client, channelList)
	// The listing names groups and channels only; the account's rooms are those and
	// its direct chats, which the cache kept.
	return a.accountRooms(ctx, account, func(domain.RoomID) bool { return true })
}

// groupListing is what one account's group listing caches: its groups as rooms, with their
// members, and its communities as spaces.
type groupListing struct {
	rooms   []domain.Room
	members map[domain.RoomID][]domain.Member
	spaces  []domain.Space
}

// saveListing writes an account's groups, with their members, over its cached rooms,
// and its communities over its cached spaces.
// What the sweep must not take is kept in the snapshot: the account's direct chats
// (the listing names groups only), and any room a message arrived in since the
// listing was fetched (a group just joined, a chat just begun) — either would lose
// its history otherwise.
func (a *Adapter) saveListing(ctx context.Context, account Account, l groupListing, fetched time.Time) error {
	if a.cache == nil {
		return nil
	}
	a.listing.Lock()
	defer a.listing.Unlock()
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	kept, err := a.keptRooms(ctx, owner, l.rooms, fetched)
	if err != nil {
		return err
	}
	if err := a.cache.SaveRooms(ctx, owner, append(slices.Clone(l.rooms), kept...)); err != nil {
		return fmt.Errorf("whatsapp: cache %s's rooms: %w", account.Name, err)
	}
	if err := a.cache.SaveSpaces(ctx, owner, l.spaces); err != nil {
		return fmt.Errorf("whatsapp: cache %s's communities: %w", account.Name, err)
	}
	for id, list := range l.members {
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

// RefreshRooms refetches every linked account's groups. An account WhatsApp will not
// list right now (a connection just dropped, a rate limit) answers from the cache, so
// one account's trouble does not empty the others' rooms; only when every account
// failed is it an error.
func (a *Adapter) RefreshRooms(ctx context.Context) ([]domain.Room, error) {
	var out []domain.Room
	var errs []error
	connected := a.connected()
	for _, c := range connected {
		rooms, err := a.refreshAccount(ctx, c.account, c.client, true)
		if err != nil {
			a.log.Warn("refresh rooms failed; answering from the cache", "account", c.account.Name, "err", err)
			errs = append(errs, err)
			if rooms, err = a.accountRooms(ctx, c.account, func(domain.RoomID) bool { return true }); err != nil {
				return nil, err
			}
		}
		out = append(out, rooms...)
	}
	if len(connected) > 0 && len(errs) == len(connected) {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// CanonicalParent is the community a WhatsApp room is in, else its account's space;
// "" for a room of no configured account, which has no space.
func (a *Adapter) CanonicalParent(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	community, err := a.communityOf(ctx, roomID)
	if err != nil || community != "" {
		return community, err
	}
	id := domain.ParseID(string(roomID))
	if id.Network != domain.ProtocolWhatsApp ||
		!slices.ContainsFunc(a.accountsNow(), func(acc Account) bool { return acc.Digits == id.Account }) {
		return "", nil
	}
	return accountSpaceID(id.Account), nil
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
	id := domain.ParseID(string(roomID))
	jid, err := types.ParseJID(id.Native)
	if err != nil || jid.Server != types.GroupServer {
		return a.Members(ctx, roomID, 0) // a direct chat's or channel's people are known
	}
	account, client, ok := a.clientFor(id.Account)
	if !ok {
		return a.Members(ctx, roomID, 0)
	}
	// The one group, not the account's listing: opening rooms must not cost listings.
	info, err := client.GetGroupInfo(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: members of %s: %w", roomID, err)
	}
	_, members := groupRooms(ctx, account.Digits, []*types.GroupInfo{info}, a.names(client))
	if a.cache != nil {
		if err := a.cache.SaveMembers(ctx, roomID, members[roomID]); err != nil {
			return nil, fmt.Errorf("whatsapp: cache members of %s: %w", roomID, err)
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

func (a *Adapter) MarkRoomUnread(context.Context, domain.RoomID, bool) error {
	return errNotYet("mark a chat unread")
}

// MessageHistory is a message's versions as the cache kept them (under [display.deleted]
// keep); WhatsApp keeps none to ask for.
func (a *Adapter) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	if a.cache == nil {
		return nil, domain.Deletion{}, nil
	}
	revisions, err := a.cache.Revisions(ctx, roomID, eventID)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("whatsapp: versions of %s: %w", eventID, err)
	}
	return revisions, domain.Deletion{}, nil
}

// Timeline is empty: WhatsApp keeps no history to page through; what kith has is in
// the cache.
func (a *Adapter) Timeline(context.Context, domain.RoomID, string, int) (domain.TimelinePage, error) {
	return domain.TimelinePage{}, nil
}

// FetchEvent is a cached message: WhatsApp has no message to fetch by ID.
func (a *Adapter) FetchEvent(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) (domain.Message, error) {
	if a.cache != nil {
		if msg, ok, err := a.cache.MessageByID(ctx, roomID, eventID); err == nil && ok {
			return msg, nil
		}
	}
	if msg, ok := a.quotedMessage(ctx, roomID, eventID); ok {
		return msg, nil
	}
	return domain.Message{}, fmt.Errorf("whatsapp: %s is not in the cache, and WhatsApp keeps no copy to ask for: %w", eventID, errNotOnWhatsApp)
}
