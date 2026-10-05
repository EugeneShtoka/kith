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

// inviteBuffer bounds the invite-set channel. Each item is the whole current set
// and any one of them supersedes the last, so a handful is ample.
const inviteBuffer = 4

// InProc is the real Backend: the mautrix client, its sync loop, the local cache
// and, once EnableEncryption runs, the E2EE crypto helper.
type InProc struct {
	client *mautrix.Client
	cache  *db.Cache
	// logger receives what the backend cannot return (see UseLogger); nil is silent.
	logger *slog.Logger
	// onSynced, when set, is called with each sync response's arrival time. Set before Start.
	onSynced func(time.Time)
	// onRoomsStale, when set, is called when a sync changes the room list, a room
	// name or the space hierarchy. Set before Start.
	onRoomsStale func()

	// machineMu guards the crypto machine EnableEncryption publishes. A degraded start
	// runs EnableEncryption on a worker goroutine while RPCs are already being served.
	// RWMutex because decryption is the hot reader; never held across a call that can
	// take it again.
	machineMu sync.RWMutex
	crypto    *cryptohelper.CryptoHelper
	// sendMu orders the client.Crypto write against sends, which mautrix reads it in:
	// sends hold it for read (see sealed), EnableEncryption for write. Separate from
	// machineMu because a send holds it across network I/O.
	sendMu sync.RWMutex
	// storeMu orders the crypto helper's client.Store swap (helper.Init moves the
	// sync position into the crypto store) against resetSyncPosition, which a
	// ClearCache RPC reaches during a degraded start. client.StateStore needs none:
	// OpenCryptoStore sets it before any RPC is served.
	storeMu sync.Mutex
	// rewind orders ClearCache's rewind against the running sync loop.
	rewind syncRewind
	// dispatchMu fences late decryptions out of Stop (see postDecrypt).
	dispatchMu sync.RWMutex
	// cryptoDB is the crypto/state store's database, nil until OpenCryptoStore. Stop
	// closes it, through the helper when there is one.
	cryptoDB *dbutil.Database
	// stopOnce: Stop is reached both from serve's return and from run's defer.
	stopOnce sync.Once
	// stopping is set as Stop begins: mautrix's decrypt retries run on goroutines
	// nothing can join, and one finishing late must not write a closing cache.
	stopping atomic.Bool

	// out is every stream the client reads (see streams.go).
	out streams

	// ver is SAS device verification (see verification.go).
	ver verifyState

	// names memoizes each room's user-ID→display-name map.
	names memberNameMemo

	// unread is each room's last-known unread state and read time (see unreadbook.go).
	unread unreadBook
	// selves are the other MXIDs that are this person (see selves.go).
	selves selves

	// held are our receipts waiting for their event; fetches asks the homeserver
	// about those that never arrive (see receipts.go).
	held    heldReceipts
	fetches receiptFetches

	// invites is the current invite set: a sync carries only changed invites, so the
	// whole set is kept to publish it whole and to suppress no-op republishes.
	invites struct {
		mu    sync.Mutex
		rooms map[domain.RoomID]domain.Room
	}

	// keepDeleted is [display.deleted] keep: whether deleted messages' text stays
	// cached. Atomic: a re-read config sets it while the sync loop reads it.
	keepDeleted atomic.Bool
	// onCached and onChanged, when set, hear a message being cached and a room's
	// cached messages changing otherwise (an edit's deletion, a redaction). Set
	// before Start.
	onCached  func(domain.Message)
	onChanged func(domain.RoomID)
}

// fromCache runs a cache read, wrapping its error; without a cache it answers the
// zero value, since every cache read has the network or an empty state behind it.
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

// KeepDeleted sets [display.deleted] keep, at startup and when the config is re-read.
func (b *InProc) KeepDeleted(keep bool) { b.keepDeleted.Store(keep) }

// New returns an InProc backend ready to Login. cache may be nil: reads then fall
// through to the network.
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

// cacheOwesRewind reports whether a cache, as opened, needs a full initial sync: it was
// rebuilt, or holds no room. Decided here, before anything can write rooms into it
// (the daemon's startup refresh, a client's RPC). A cache that cannot say is taken as
// empty: a needless rewind costs one full sync, a missed one the history it lost.
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

// Login authenticates with a password, stores the credentials on the client,
// and returns the session the caller persists for later Resume.
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

// Resume rebuilds an authenticated client from a saved session and verifies the
// token with a /whoami round-trip, so an expired token surfaces here rather
// than on the first real call.
func (b *InProc) Resume(ctx context.Context, s domain.Session) error {
	client, err := mautrix.NewClient(s.Homeserver, id.UserID(s.UserID), s.AccessToken)
	if err != nil {
		return fmt.Errorf("matrix: new client: %w", err)
	}
	client.DeviceID = id.DeviceID(s.DeviceID)
	b.attachLogger(client)
	err = whoami(ctx, client)
	// Keep the client unless the token was rejected: the daemon serves from the
	// cache while offline (see Reachable).
	if !errors.Is(err, api.ErrSessionRejected) {
		b.client = client
	}
	return err
}

// whoami validates the session. A rejected token wants a new login; an unreachable
// homeserver says nothing about the token and must not cost a new device identity.
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

// Reachable reports whether the homeserver answers, classified like Resume. Unlike
// Resume it does not replace b.client, so it is safe while serving.
func (b *InProc) Reachable(ctx context.Context) error {
	if b.client == nil {
		return api.ErrUnreachable
	}
	return whoami(ctx, b.client)
}

// Start registers the sync handlers and runs the /sync loop until ctx is canceled or
// Stop is called. It blocks.
// errStartedTwice refuses a second Start: a backend syncs once (the rewind restarts
// the loop inside Start, see syncUntilDone).
var errStartedTwice = errors.New("matrix: Start runs once per backend")

func (b *InProc) Start(ctx context.Context) error {
	// Closing the streams on return lets listeners see a clean end (see streams.close).
	defer b.out.close()
	var syncer *mautrix.DefaultSyncer
	switch current := b.client.Syncer.(type) {
	case *mautrix.DefaultSyncer:
		syncer = current
	case *failingSyncer:
		// A second Start would register every handler again (each event handled
		// twice), on streams the first closed when it returned.
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
	// Handlers are registered on syncer itself; the wrapper only adds logging.
	b.client.Syncer = &failingSyncer{DefaultSyncer: syncer, b: b}
	if err := b.syncUntilDone(ctx); err != nil {
		return fmt.Errorf("matrix: sync: %w", err)
	}
	return nil
}

// Stop halts the sync loop and releases resources (verification work, crypto store,
// spell engine, local model). The caller must first cancel and join the sync loop:
// StopSync does not wait. Idempotent and safe before Login/Resume.
func (b *InProc) Stop() {
	b.stopOnce.Do(func() {
		b.beginStopping()
		if b.client != nil {
			b.client.StopSync()
		}
		// Cancel, join, then close the store: verification goroutines use it.
		b.ver.stop()
		// Read-position fetches write the cache; the sync context that bounds them is
		// already canceled.
		b.fetches.wg.Wait()

		// A store that fails to close may not have flushed; worth the journal.
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

// onRedaction handles a live m.room.redaction: an un-react when the target is a
// cached reaction, else it flags the cached message and emits a Redacted stub that
// the client folds onto the displayed row.
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
		// An edit folds onto the message it replaces: learn which one first, since
		// MarkRedacted takes its words off it.
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
	// The kept words travel with the redaction, so "forget it" reaches the screen too.
	gone := domain.Message{
		ID: target, RoomID: roomID, Redacted: true,
		RedactedBy: string(evt.Sender), RedactedReason: redactionReason(evt),
		// The redaction's time, i.e. when it was deleted.
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

// emitReverted sends a message whose shown edit was deleted as the cache now has it,
// marked to replace what the client shows. When the cache held no older version (it
// keeps them only under [display.deleted] keep), the message shows nothing; the
// version before the deleted edit is then asked of the server in the background,
// cached, and sent again.
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
	// One fetch per deleted edit, and no more at once than the receipt fetches: a
	// moderator deleting many edits, or a full sync, must not fan out a request each.
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

// onSync ingests each joined room's unread counts, receipts, marked-unread, stars,
// spam verdicts and typing from a sync response, caching and streaming changes.
func (b *InProc) onSync(ctx context.Context, resp *mautrix.RespSync, since string) bool {
	if b.onSynced != nil {
		b.onSynced(time.Now())
	}
	// Reported, not acted on: refreshing is a network call on the sync goroutine.
	if b.onRoomsStale != nil && roomsChanged(resp, b.client.UserID) {
		b.onRoomsStale()
	}
	b.syncInvites(ctx, resp, since)
	// Receipts still held from earlier syncs are not arriving on their own.
	b.fetchHeld(ctx)
	for roomID, jr := range resp.Rooms.Join {
		if jr == nil {
			continue
		}
		// One read serves both the room marker and the threads: only together can an
		// unthreaded receipt (moves both) be told from a "main" one.
		receipts := readReceipts(jr.Ephemeral.Events, b.client.UserID)
		read, threadsMoved := b.applyReceipts(ctx, domain.RoomID(roomID), receipts)
		// m.marked_unread; a batch without it says nothing (see markedUnread).
		var marked *bool
		if flag, present := markedUnread(jr.AccountData.Events); present {
			marked = &flag
		}
		// A star or spam event type present in the batch is that room's whole set;
		// absence is silence.
		if ids, present := starredFrom(jr.AccountData.Events); present && b.cache != nil {
			b.warnIf(ctx, b.cache.SetStarred(ctx, domain.RoomID(roomID), ids, time.Now().UnixMilli()), "cache starred messages", "room", roomID)
		}
		if verdict, present := spamFrom(jr.AccountData.Events); present {
			b.spamMirror(ctx, domain.RoomID(roomID), verdict)
		}
		b.emitActivity(domain.RoomID(roomID), jr.Ephemeral.Events)
		if _, changed := b.mergeUnread(ctx, domain.RoomID(roomID), jr, read, marked); !changed && !threadsMoved {
			continue
		}
		// Receipts moved: recount. This response's messages are dispatched after OnSync,
		// and onMessage recounts again then.
		b.recountAndEmit(ctx, domain.RoomID(roomID), true)
	}
	return true
}

// OnCached sets who hears each message the backend caches, and each room whose
// cached messages changed otherwise. Set before Start.
func (b *InProc) OnCached(cached func(domain.Message), changed func(domain.RoomID)) {
	b.onCached, b.onChanged = cached, changed
}

// changed tells the listener a room's cached messages changed.
func (b *InProc) changed(roomID domain.RoomID) {
	if b.onChanged != nil {
		b.onChanged(roomID)
	}
}

// RewindSync rewinds the sync position so a full initial sync refills an emptied
// cache.
func (b *InProc) RewindSync(ctx context.Context) error {
	// Owed as well as done: before Start, the store rewound now may not be the one it
	// syncs on (EnableEncryption swaps in the crypto store). On failure the cache stays
	// empty, so a restart rewinds too.
	b.rewind.owe()
	return b.rewind.request(func() error { return b.resetSyncPosition(ctx) })
}

// resyncEmptiedCache rewinds to a full initial sync when one is owed: the cache was
// rebuilt or held no room when the backend was built, or was cleared since. The token
// lives in the sync store (the crypto store), not the cache, so a cache emptied
// without the rewind would never get its history back. Emptiness is the durable
// half: a crash between a rebuild and this, or a rewind that failed, leaves the cache
// empty, and the next start rewinds again. For a new account there is nothing to
// lose. A failed rewind stops Start rather than syncing on from the old token.
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

// resetSyncPosition rewinds to a full initial sync. The token is in the sync store
// (the crypto store after EnableEncryption), not this cache.
func (b *InProc) resetSyncPosition(ctx context.Context) error {
	if b.client == nil {
		return nil
	}
	// Held for write: mautrix's crypto store saves the token without a lock of its
	// own, and the crypto helper's Init swaps client.Store under the same mutex.
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

// OnSynced registers a callback with each sync response's arrival time (readiness).
// Call before Start; it runs on the sync goroutine and must not block.
func (b *InProc) OnSynced(fn func(time.Time)) { b.onSynced = fn }

// OnRoomsStale registers a callback for syncs that change the room list, names,
// spaces or m.direct, since only RefreshRooms/RefreshSpaces write those caches.
// Call before Start; it runs on the sync goroutine and must not block.
func (b *InProc) OnRoomsStale(fn func()) { b.onRoomsStale = fn }

// receiptType picks m.read.private when private: our position moves for this
// account's clients without the room seeing it.
func receiptType(private bool) event.ReceiptType {
	if private {
		return event.ReceiptTypeReadPrivate
	}
	return event.ReceiptTypeRead
}
