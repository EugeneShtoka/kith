package matrix

import (
	"context"
	"errors"
	"sync"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A message whose megolm session kith lacks is a stand-in until its key comes. Its
// key is asked for once per session, in the background: from the key backup when
// this device holds the backup key (shared by a verified device), else by a room-key
// request to our other devices and the sender's. When the session arrives, every
// event waiting on it is decrypted, cached and handed on, replacing its stand-in.

// keyWait is how long a session is waited for after asking.
const keyWait = 10 * time.Minute

// keyWaits gathers undecryptable events by session: one ask per session.
type keyWaits struct {
	mu sync.Mutex
	wg sync.WaitGroup
	// ctx is the sync loop's, set by Start: asks outlive the RPC that found the event,
	// and end with the loop.
	ctx     context.Context //nolint:containedctx // asks found by an RPC must outlive it, and end with the loop
	waiting map[id.SessionID]map[id.EventID]*event.Event
}

// start arms the asks with the sync loop's context.
func (k *keyWaits) start(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ctx = ctx
}

// add records an event waiting on its session, reporting whether it is the first (the
// one to ask) and the context to ask under; nil before Start.
func (k *keyWaits) add(session id.SessionID, evt *event.Event) (context.Context, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ctx == nil || k.ctx.Err() != nil {
		return nil, false
	}
	if k.waiting == nil {
		k.waiting = map[id.SessionID]map[id.EventID]*event.Event{}
	}
	events, asking := k.waiting[session]
	if !asking {
		events = map[id.EventID]*event.Event{}
		k.waiting[session] = events
	}
	events[evt.ID] = evt
	return k.ctx, !asking
}

// take is every event waiting on a session; the next one asks again.
func (k *keyWaits) take(session id.SessionID) []*event.Event {
	k.mu.Lock()
	defer k.mu.Unlock()
	events := k.waiting[session]
	delete(k.waiting, session)
	out := make([]*event.Event, 0, len(events))
	for _, evt := range events {
		out = append(out, evt)
	}
	return out
}

// wantKey asks for an undecryptable event's session unless already asked.
func (b *InProc) wantKey(evt *event.Event) {
	helper := b.cryptoHelper()
	if helper == nil || evt.Type != event.EventEncrypted {
		return
	}
	content := evt.Content.AsEncrypted()
	if content.SessionID == "" {
		return
	}
	ctx, first := b.keys.add(content.SessionID, evt)
	if !first {
		return
	}
	mach := helper.Machine()
	b.keys.wg.Go(func() { b.fetchKey(ctx, mach, evt.RoomID, evt.Sender, content) })
}

// onDecryptError takes up a live event the crypto helper gave up waiting for.
func (b *InProc) onDecryptError(evt *event.Event, err error) {
	if errors.Is(err, cryptohelper.NoSessionFound) {
		b.wantKey(evt)
	}
}

// fetchKey gets one session from the backup or asks for it, then waits for it and
// decrypts what waits on it.
func (b *InProc) fetchKey(ctx context.Context, mach *crypto.OlmMachine, roomID id.RoomID, sender id.UserID, content *event.EncryptedEventContent) {
	session := content.SessionID
	// Held already: it came while this event was being fetched (or asked for before).
	held, _ := mach.CryptoStore.GetGroupSession(ctx, roomID, session)
	if held == nil && !b.sessionFromBackup(ctx, mach, roomID, session) {
		err := mach.SendRoomKeyRequest(ctx, roomID, content.SenderKey, session, "", map[id.UserID][]id.DeviceID{
			sender:          {"*"},
			b.client.UserID: {"*"},
		})
		b.warnIf(ctx, err, "ask for a room key", "room", roomID, "session", session)
	}
	arrived := mach.WaitForSession(ctx, roomID, content.SenderKey, session, keyWait)
	for _, evt := range b.keys.take(session) {
		if arrived {
			b.redecrypt(ctx, mach, evt)
		}
	}
}

// sessionFromBackup imports one session from the key backup, when this device holds
// the backup key. It reports whether the session is now held.
func (b *InProc) sessionFromBackup(ctx context.Context, mach *crypto.OlmMachine, roomID id.RoomID, session id.SessionID) bool {
	secret, err := mach.CryptoStore.GetSecret(ctx, id.SecretMegolmBackupV1)
	if err != nil || secret == "" {
		return false
	}
	key, ok := parseBackupKey(secret)
	if !ok {
		return false
	}
	version, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, key)
	if err != nil || version == nil {
		b.debugIf(ctx, err, "read the key backup's version")
		return false
	}
	data, err := b.client.GetKeyBackupForRoomAndSession(ctx, version.Version, roomID, session)
	if err != nil {
		b.debugIf(ctx, err, "fetch a room key from the backup", "room", roomID, "session", session)
		return false
	}
	decrypted, err := data.SessionData.Decrypt(key)
	if err != nil {
		b.warnIf(ctx, err, "open a room key from the backup", "room", roomID, "session", session)
		return false
	}
	_, err = mach.ImportRoomKeyFromBackup(ctx, version.Version, roomID, session, decrypted)
	b.warnIf(ctx, err, "import a room key from the backup", "room", roomID, "session", session)
	return err == nil
}

// redecrypt decrypts an event whose session arrived, caches it in place of its stand-in
// and hands it on as late (it may be old: notifications fold it).
func (b *InProc) redecrypt(ctx context.Context, mach *crypto.OlmMachine, evt *event.Event) {
	decrypted, err := mach.DecryptMegolmEvent(ctx, evt)
	if err != nil {
		b.debugIf(ctx, err, "decrypt after the key came", "event", evt.ID)
		return
	}
	// Not into a closing cache (see postDecrypt).
	b.dispatchMu.RLock()
	defer b.dispatchMu.RUnlock()
	if b.stopping.Load() {
		return
	}
	roomID := domain.RoomID(evt.RoomID)
	msg, undecrypted, ok := b.fetchedMessage(ctx, roomID, decrypted)
	if !ok || undecrypted {
		return
	}
	b.cacheMessages(ctx, roomID, []domain.Message{msg})
	if b.cache != nil && msg.Media != nil {
		b.saveMediaSource(ctx, roomID, decrypted)
	}
	if b.onCached != nil {
		b.onCached(msg)
	}
	b.settleReceipts(ctx, roomID)
	b.recount(ctx, roomID)
	msg.Late = true
	emit(&b.out, b.out.msgs, msg)
}
