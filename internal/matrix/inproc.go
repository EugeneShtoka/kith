// Package matrix adapts mautrix-go to api.Backend. It is the only package that
// imports the Matrix SDK; SDK types become domain types at the boundary.
package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// messageBuffer bounds the sync→UI channel; if the UI stalls, events are
// dropped rather than blocking the sync goroutine.
const messageBuffer = 64

// inviteBuffer bounds the invite-set channel; each item is the whole set, so a few suffice.
const inviteBuffer = 4

// InProc is the real Backend: the mautrix client, its sync loop, the local cache
// and, once EnableEncryption runs, the E2EE crypto helper.
type InProc struct {
	client *mautrix.Client
	cache  *db.Cache
	// tagsMu serializes read-change-write of tag account data (tagverdicts.go).
	tagsMu sync.Mutex
	// logger receives what the backend cannot return (see UseLogger); nil is silent.
	logger *slog.Logger
	// onSynced, when set, is called with each sync response's arrival time. Set before Start.
	onSynced func(time.Time)
	// onRoomsStale, when set, is called when a sync changes the room list, a room
	// name or the space hierarchy. Set before Start.
	onRoomsStale func()
	// left is when each room was left here, so an in-flight refresh does not bring it back.
	left leftRooms
	// listing is the joined-rooms listing in flight, which concurrent refreshes share;
	// types is each room's m.room.create type, read once (spaces.go).
	listing sharedListing
	types   roomTypes
	// networks reads each room's network in the background (networks.go).
	networks roomNetworks
	// reads is the read receipts the homeserver has not taken yet (readowed.go).
	reads owedReads

	// machineMu guards the crypto machine EnableEncryption publishes, possibly while RPCs
	// are served (degraded start). Never held across a call that can take it again.
	machineMu sync.RWMutex
	crypto    *cryptohelper.CryptoHelper
	// sendMu orders the client.Crypto write against sends that read it (see sealed).
	// Separate from machineMu because a send holds it across network I/O.
	sendMu sync.RWMutex
	// storeMu orders helper.Init's client.Store swap against resetSyncPosition (a
	// ClearCache RPC during a degraded start).
	storeMu sync.Mutex
	// rewind orders ClearCache's rewind against the running sync loop.
	rewind syncRewind
	// dispatchMu fences late decryptions out of Stop (see postDecrypt).
	dispatchMu sync.RWMutex
	// cryptoDB is the crypto/state store's database, nil until OpenCryptoStore.
	cryptoDB *dbutil.Database
	// stopOnce: Stop is reached both from serve's return and from run's defer.
	stopOnce sync.Once
	// stopping is set as Stop begins: unjoinable decrypt retries must not write a closing cache.
	stopping atomic.Bool

	out streams

	ver verifyState

	// names memoizes each room's user-ID→display-name map; profiles memoizes the names of
	// senders who are no member of the room they wrote in (rooms.go).
	names    memberNameMemo
	profiles profileNames

	unread unreadBook
	// selves are the other MXIDs that are this person.
	selves selves

	// held are our receipts waiting for their event; fetches asks the homeserver for
	// those that never arrive.
	held    heldReceipts
	fetches receiptFetches

	// gaps fills the history syncs skipped (gaps.go); syncs counts responses, the first
	// being the catch-up after a start.
	gaps  gapFiller
	syncs atomic.Int64
	// onSyncTrouble hears a sync failing the same way again and again (logs.go); a
	// sync that then succeeds says online again.
	onSyncTrouble func(detail string)

	// invites is the whole invite set; a sync carries only changes.
	invites struct {
		mu    sync.Mutex
		rooms map[domain.RoomID]domain.Room
	}

	// keepDeleted is [display.deleted] keep; atomic because a config reload sets it.
	keepDeleted atomic.Bool
	// onCached and onChanged hear a message cached and a room's messages otherwise
	// changed (redaction, deleted edit). Set before Start.
	onCached  func(domain.Message)
	onChanged func(domain.RoomID)
}

// fromCache runs a cache read, wrapping its error; without a cache it returns the zero value.
func fromCache[T any](b *InProc, op string, read func(*db.Cache) (T, error)) (T, error) {
	var zero T
	if b.cache == nil {
		return zero, nil
	}
	v, err := read(b.cache)
	if err != nil {
		return zero, fmt.Errorf("matrix: %s: %w", op, err)
	}
	return v, nil
}

// toCache runs a cache write, wrapping its error; a no-op without a cache.
func toCache(b *InProc, op string, write func(*db.Cache) error) error {
	if b.cache == nil {
		return nil
	}
	if err := write(b.cache); err != nil {
		return fmt.Errorf("matrix: %s: %w", op, err)
	}
	return nil
}

// KeepDeleted sets [display.deleted] keep.
func (b *InProc) KeepDeleted(keep bool) { b.keepDeleted.Store(keep) }

// New returns an InProc backend ready to Login; a nil cache sends reads to the network.
func New(cache *db.Cache) *InProc {
	b := &InProc{
		client: nil,
		cache:  cache,
	}
	b.out.open()
	b.invites.rooms = make(map[domain.RoomID]domain.Room)
	b.ver.events = make(chan domain.Verification, verificationBuffer)
	if cacheOwesRewind(cache) {
		b.rewind.owe()
	}
	return b
}

// cacheOwesRewind reports whether a freshly opened cache (rebuilt or roomless) needs a
// full initial sync. Decided before anything writes rooms; an error counts as empty,
// since a needless rewind costs one sync and a missed one loses history.
func cacheOwesRewind(cache *db.Cache) bool {
	if cache == nil {
		return false
	}
	if cache.Rebuilt() {
		return true
	}
	holds, err := cache.HoldsRooms(context.Background())
	return err != nil || !holds
}

// Login authenticates with a password and returns the session to persist for Resume.
func (b *InProc) Login(ctx context.Context, homeserver, user, password string) (domain.Session, error) {
	client, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: new client: %w", err)
	}
	b.attachLogger(client)
	resp, err := client.Login(ctx, &mautrix.ReqLogin{
		Type:             mautrix.AuthTypePassword,
		Identifier:       mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Password:         password,
		StoreCredentials: true,
	})
	if err != nil {
		return domain.Session{}, fmt.Errorf("matrix: login: %w", err)
	}
	b.client = client
	return domain.Session{
		Homeserver:  homeserver,
		UserID:      string(resp.UserID),
		DeviceID:    string(resp.DeviceID),
		AccessToken: resp.AccessToken,
	}, nil
}

// Resume rebuilds a client from a saved session and checks the token with /whoami.
func (b *InProc) Resume(ctx context.Context, s domain.Session) error {
	client, err := mautrix.NewClient(s.Homeserver, id.UserID(s.UserID), s.AccessToken)
	if err != nil {
		return fmt.Errorf("matrix: new client: %w", err)
	}
	client.DeviceID = id.DeviceID(s.DeviceID)
	b.attachLogger(client)
	err = whoami(ctx, client)
	// Keep the client unless the token was rejected: offline, the daemon serves from cache.
	if !errors.Is(err, api.ErrSessionRejected) {
		b.client = client
	}
	return err
}

// whoami validates the session. Only a rejected token wants a new login; an unreachable
// homeserver must not cost a new device identity.
func whoami(ctx context.Context, client *mautrix.Client) error {
	_, err := client.Whoami(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mautrix.MUnknownToken) || errors.Is(err, mautrix.MMissingToken):
		return fmt.Errorf("%w: %w", api.ErrSessionRejected, err)
	default:
		return fmt.Errorf("%w: %w", api.ErrUnreachable, err)
	}
}

// Reachable reports whether the homeserver answers, like Resume but without replacing
// b.client, so it is safe while serving.
func (b *InProc) Reachable(ctx context.Context) error {
	if b.client == nil {
		return api.ErrUnreachable
	}
	return whoami(ctx, b.client)
}

// errStartedTwice refuses a second Start; a rewind restarts the loop inside Start.
var errStartedTwice = errors.New("matrix: Start runs once per backend")

// Start registers the sync handlers and runs the /sync loop until ctx is canceled or
// Stop is called. It blocks.
func (b *InProc) Start(ctx context.Context) error {
	defer b.out.close()
	var syncer *mautrix.DefaultSyncer
	switch current := b.client.Syncer.(type) {
	case *mautrix.DefaultSyncer:
		syncer = current
	case *failingSyncer:
		// A second Start would double-register handlers on already closed streams.
		return errStartedTwice
	default:
		return fmt.Errorf("matrix: unexpected syncer type %T", b.client.Syncer)
	}
	if err := b.resyncEmptiedCache(ctx); err != nil {
		return err
	}
	b.seedUnread(ctx)
	b.seedInvites(ctx)
	syncer.OnEventType(event.EventMessage, b.onMessage)
	// Stickers render as messages, but only if the syncer delivers them.
	syncer.OnEventType(event.EventSticker, b.onMessage)
	syncer.OnEventType(event.EventRedaction, b.onRedaction)
	syncer.OnEventType(event.EventReaction, b.onReaction)
	syncer.OnEventType(event.StateMember, b.onMember)
	syncer.OnSync(b.onSync)
	b.wantNetworks()
	// Gaps the last run left unfilled are taken up on the first sync.
	b.gaps.pending.Store(true)
	// Handlers are registered on syncer itself; the wrapper only adds logging.
	b.client.Syncer = &failingSyncer{DefaultSyncer: syncer, b: b}
	if err := b.syncUntilDone(ctx); err != nil {
		return fmt.Errorf("matrix: sync: %w", err)
	}
	return nil
}

// Stop halts the sync loop and releases resources. The caller must first cancel and
// join the sync loop (StopSync does not wait). Idempotent; safe before Login/Resume.
func (b *InProc) Stop() {
	b.stopOnce.Do(func() {
		b.beginStopping()
		if b.client != nil {
			b.client.StopSync()
		}
		// Cancel, join, then close the store: verification goroutines use it.
		b.ver.stop()
		// Read-position fetches write the cache; their context is already canceled.
		b.fetches.wg.Wait()
		b.gaps.wg.Wait()

		if helper := b.cryptoHelper(); helper != nil {
			b.warnIf(context.Background(), helper.Close(), "close the crypto store")
		} else if b.cryptoDB != nil {
			b.warnIf(context.Background(), b.cryptoDB.Close(), "close the crypto store")
		}
	})
}

// onMessage caches a live message and hands it to the UI (dropped if the buffer is full).
func (b *InProc) onMessage(ctx context.Context, evt *event.Event) {
	msg, ok := toDomainMessage(evt)
	if !ok {
		return
	}
	msg.Mentioned = mentionsMe(evt, b.client.UserID)
	msg.SenderName = b.senderName(ctx, msg.RoomID, msg.Sender)
	if b.cache != nil {
		b.cacheMessages(ctx, msg.RoomID, []domain.Message{msg})
		if b.onCached != nil {
			b.onCached(msg)
		}
		if msg.Media != nil {
			b.saveMediaSource(ctx, msg.RoomID, evt)
		}
		// Place held receipts before recounting, or a read reply is counted.
		b.settleReceipts(ctx, msg.RoomID)
		// Recount locally: a muted room's server count never moves.
		b.recount(ctx, msg.RoomID)
	}
	emit(&b.out, b.out.msgs, msg)
}

// onRedaction handles a live redaction: an un-react for a cached reaction, else it
// marks the message redacted and emits a Redacted stub.
func (b *InProc) onRedaction(ctx context.Context, evt *event.Event) {
	target := redactionTarget(evt)
	if target == "" {
		return
	}
	roomID := domain.RoomID(evt.RoomID)
	if b.cache != nil {
		r, ok, derr := b.cache.DeleteReaction(ctx, target)
		b.warnIf(ctx, derr, "delete cached reaction", "room", roomID, "event", target)
		if ok {
			b.noteRefusedReaction(ctx, r, evt)
			emit(&b.out, b.out.reactions, domain.ReactionUpdate{Reaction: r, Removed: true})
			return
		}
		// Find the edit's target before MarkRedacted strips its body.
		shownOn, isEdit, serr := b.cache.EditShownBy(ctx, roomID, target)
		b.warnIf(ctx, serr, "find the message a deleted edit shows on", "room", roomID, "event", target)
		b.warnIf(ctx, b.cache.MarkRedacted(ctx, roomID, target, string(evt.Sender), redactionReason(evt),
			time.UnixMilli(evt.Timestamp), b.keepDeleted.Load()),
			"cache redaction", "room", roomID, "event", target)
		b.changed(roomID)
		b.recount(ctx, roomID)
		if isEdit {
			b.emitReverted(ctx, roomID, shownOn, target)
			return
		}
	}
	gone := domain.Message{
		ID: target, RoomID: roomID, Redacted: true,
		RedactedBy: string(evt.Sender), RedactedReason: redactionReason(evt),
		RedactedAt: eventTime(evt.Timestamp),
	}
	if b.keepDeleted.Load() && b.cache != nil {
		kept, ok, kerr := b.cache.Message(ctx, roomID, target)
		b.warnIf(ctx, kerr, "read the kept copy of a deleted message", "room", roomID, "event", target)
		if ok {
			gone.Body, gone.Format = kept.Body, kept.Format
		}
	}
	emit(&b.out, b.out.msgs, gone)
}

// emitReverted re-sends a message whose shown edit was deleted. Without a cached older
// version (kept only under [display.deleted] keep) it is fetched in the background,
// cached and sent again.
func (b *InProc) emitReverted(ctx context.Context, roomID domain.RoomID, target, deleted domain.EventID) {
	send := func() (domain.Message, bool) {
		msg, ok, err := b.cache.MessageByID(ctx, roomID, target)
		b.warnIf(ctx, err, "read a message after its edit was deleted", "room", roomID, "event", target)
		if !ok {
			return domain.Message{}, false
		}
		msg.Reverted = true
		emit(&b.out, b.out.msgs, msg)
		return msg, true
	}
	msg, ok := send()
	if !ok || msg.Body != "" || msg.Redacted || b.client == nil {
		return
	}
	// One fetch per deleted edit, sharing the receipt fetches' concurrency limit.
	slots, first := b.fetches.first(fetchKey{room: roomID, event: deleted})
	if !first {
		return
	}
	b.fetches.wg.Go(func() {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-slots }()
		revs, _ := b.fetchRevisions(ctx, roomID, target)
		prev, found := newestVersionBut(revs, deleted)
		if !found {
			return
		}
		if err := b.cache.ShowVersion(ctx, roomID, target, prev); err != nil {
			b.warnIf(ctx, err, "restore the version before a deleted edit", "room", roomID, "event", target)
			return
		}
		send()
	})
}

// newestVersionBut is the newest of revs other than the deleted edit.
func newestVersionBut(revs []domain.Revision, deleted domain.EventID) (domain.Revision, bool) {
	var best domain.Revision
	found := false
	for _, r := range revs {
		if r.ID == deleted || r.Body == "" {
			continue
		}
		if !found || r.At.After(best.At) || (r.At.Equal(best.At) && r.ID > best.ID) {
			best, found = r, true
		}
	}
	return best, found
}

// onSync ingests the person verdicts, and each joined room's unread counts, receipts,
// marked-unread, stars, tag verdicts and typing.
func (b *InProc) onSync(ctx context.Context, resp *mautrix.RespSync, since string) bool {
	if b.onSynced != nil {
		b.onSynced(time.Now())
	}
	b.syncs.Add(1)
	// Before the token moves on: the gap is kept even if kithd stops right after.
	b.recordGaps(ctx, resp, since)
	b.readNetworksIfWanted(ctx)
	// A sync succeeded: the homeserver is there for receipts owed. Not on this goroutine.
	if len(b.reads.owed()) > 0 {
		go b.sendOwed(context.WithoutCancel(ctx))
	}
	// Reported, not acted on: refreshing is a network call on the sync goroutine.
	if b.onRoomsStale != nil && roomsChanged(resp, b.client.UserID) {
		b.onRoomsStale()
	}
	// The people account data present is the whole set; absence means no change.
	if content, present := peopleFrom(resp.AccountData.Events); present {
		b.mirrorPeople(ctx, content.verdicts())
	}
	b.syncInvites(ctx, resp, since)
	b.fetchHeld(ctx)
	for roomID, jr := range resp.Rooms.Join {
		if jr == nil {
			continue
		}
		// Room and thread receipts are read together: only then can an unthreaded
		// receipt be told from a "main" one.
		receipts := readReceipts(jr.Ephemeral.Events, b.client.UserID)
		read, threadsMoved := b.applyReceipts(ctx, domain.RoomID(roomID), receipts)
		var marked *bool
		if flag, present := markedUnread(jr.AccountData.Events); present {
			marked = &flag
		}
		// A star or tags event present is the room's whole set; absence means no change.
		if ids, present := starredFrom(jr.AccountData.Events); present && b.cache != nil {
			b.warnIf(ctx, b.cache.SetStarred(ctx, domain.RoomID(roomID), ids, time.Now().UnixMilli()), "cache starred messages", "room", roomID)
		}
		if content, present := tagsFrom(jr.AccountData.Events); present {
			b.mirrorVerdicts(ctx, domain.RoomID(roomID), content.verdicts(domain.RoomID(roomID)))
		}
		b.emitActivity(domain.RoomID(roomID), jr.Ephemeral.Events)
		if _, changed := b.mergeUnread(ctx, domain.RoomID(roomID), jr, read, marked); !changed && !threadsMoved {
			continue
		}
		// This response's messages come after OnSync; onMessage recounts again then.
		b.recountAndEmit(ctx, domain.RoomID(roomID), true)
	}
	return true
}

// OnCached sets the listeners for cached messages and otherwise changed rooms. Set before Start.
func (b *InProc) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	b.onCached, b.onChanged = cached, changed
}

func (b *InProc) changed(roomID domain.RoomID) {
	if b.onChanged != nil {
		b.onChanged(roomID)
	}
}

// RewindSync rewinds the sync position so a full initial sync refills an emptied
// cache.
func (b *InProc) RewindSync(ctx context.Context) error {
	// Also owed: before Start, EnableEncryption may swap in a different store.
	b.rewind.owe()
	return b.rewind.request(func() error { return b.resetSyncPosition(ctx) })
}

// resyncEmptiedCache rewinds to a full initial sync when owed (cache rebuilt, empty or
// cleared). The token lives in the sync store, not the cache, so an emptied cache needs
// this to regain history. A failed rewind stops Start rather than syncing on.
func (b *InProc) resyncEmptiedCache(ctx context.Context) error {
	if b.cache == nil || !b.rewind.isOwed() {
		return nil
	}
	if err := b.resetSyncPosition(ctx); err != nil {
		return err
	}
	b.rewind.paid()
	return nil
}

// resetSyncPosition clears the sync token in the sync store (the crypto store once encrypted).
func (b *InProc) resetSyncPosition(ctx context.Context) error {
	if b.client == nil {
		return nil
	}
	// mautrix's crypto store saves the token unlocked, and Init swaps client.Store under this.
	b.storeMu.Lock()
	defer b.storeMu.Unlock()
	store := b.client.Store
	if store == nil {
		return nil
	}
	if err := store.SaveNextBatch(ctx, b.client.UserID, ""); err != nil {
		return fmt.Errorf("matrix: reset the sync position: %w", err)
	}
	return nil
}

// OnSynced registers a callback with each sync response's arrival time. Call before
// Start; it runs on the sync goroutine and must not block.
func (b *InProc) OnSynced(fn func(time.Time)) { b.onSynced = fn }

// OnRoomsStale registers a callback for syncs that change rooms, names, spaces or
// m.direct. Call before Start; it runs on the sync goroutine and must not block.
func (b *InProc) OnRoomsStale(fn func()) { b.onRoomsStale = fn }

// receiptType picks m.read.private (visible only to this account) when private.
func receiptType(private bool) event.ReceiptType {
	if private {
		return event.ReceiptTypeReadPrivate
	}
	return event.ReceiptTypeRead
}
