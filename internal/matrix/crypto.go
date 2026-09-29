package matrix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/sqlstatestore"

	_ "modernc.org/sqlite" // pure-Go "sqlite" driver for the crypto store

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// cryptoDSN is the crypto store's connection string (as internal/db.dsn: per-connection
// pragmas on the DSN, path escaped).
func cryptoDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	return u.String()
}

// OpenCryptoStore opens mautrix's crypto/state store (its own SQLite file) and
// installs the room state store on the client. It is local, so it runs before any
// RPC is served, even when the homeserver is down: mautrix reads client.StateStore
// with no lock after most calls, so it must never change once handlers run. Call
// after Resume. Stop closes the store.
func (b *InProc) OpenCryptoStore(ctx context.Context, dbPath string) error {
	// Every connection setting is on the DSN: foreign_keys and busy_timeout are
	// per-connection, and this store's corruption is not recoverable.
	rawDB, err := sql.Open("sqlite", cryptoDSN(dbPath))
	if err != nil {
		return fmt.Errorf("matrix: open crypto db: %w", err)
	}
	rawDB.SetMaxOpenConns(1)

	cryptoDB, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		_ = rawDB.Close() // ignored: cleanup; the wrap error is the one to report
		return fmt.Errorf("matrix: wrap crypto db: %w", err)
	}
	log := b.client.Log.With().Str("component", "crypto").Logger()
	states := sqlstatestore.NewSQLStateStore(cryptoDB,
		dbutil.ZeroLogger(log.With().Str("db_section", "matrix_state").Logger()), false)
	if err := states.Upgrade(ctx); err != nil {
		_ = cryptoDB.Close() // ignored: cleanup; the upgrade error is the one to report
		return fmt.Errorf("matrix: upgrade the state store: %w", err)
	}
	syncer, ok := b.client.Syncer.(mautrix.ExtensibleSyncer)
	if !ok {
		_ = cryptoDB.Close() // ignored: cleanup; the syncer error is the one to report
		return errors.New("matrix: open crypto store: the syncer cannot take handlers")
	}
	b.client.StateStore = states
	// The crypto helper feeds only a state store it made itself; this one is ours,
	// and without the feed a room that turns on encryption would read as plain.
	syncer.OnEvent(b.client.StateStoreSyncHandler)
	b.cryptoDB = cryptoDB
	return nil
}

// EnableEncryption builds the crypto helper on the store OpenCryptoStore opened and
// wires it into the client, so sends encrypt and events decrypt. pickleKey must be
// stable across runs. It needs the homeserver, so a degraded start calls it once the
// homeserver answers, while RPCs are live. Failure may be treated as non-fatal: the
// state store stays, and encrypted rooms refuse sends (see sealed).
func (b *InProc) EnableEncryption(ctx context.Context, pickleKey []byte) error {
	if b.cryptoDB == nil {
		return errors.New("matrix: enable encryption: the crypto store is not open")
	}
	// client.StateStore is set, so the helper keeps it rather than installing its own.
	helper, err := cryptohelper.NewCryptoHelper(b.client, pickleKey, b.cryptoDB)
	if err != nil {
		return fmt.Errorf("matrix: new crypto helper: %w", err)
	}
	helper.CustomPostDecrypt = b.postDecrypt
	// Init moves the sync position into the crypto store (client.Store).
	b.storeMu.Lock()
	err = helper.Init(ctx)
	b.storeMu.Unlock()
	if err != nil {
		// Not closed: the state store still reads the same database; Stop closes it.
		return fmt.Errorf("matrix: init crypto: %w", err)
	}
	b.wireCrypto(helper)
	b.publishCrypto(helper)
	// Verification failure is non-fatal but kept (VerificationUnavailable).
	b.ver.setErr(b.enableVerification(ctx, helper))
	return nil
}

// wireCrypto hands mautrix the crypto machine. Sends read client.Crypto inside mautrix,
// so the write takes sendMu; b.crypto is published separately (see machineMu).
func (b *InProc) wireCrypto(c mautrix.CryptoHelper) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.client.Crypto = c
}

// sealed runs send with client.Crypto fixed for its length, refusing an encrypted room
// while there is no crypto machine: mautrix then posts the event in the clear.
func (b *InProc) sealed(ctx context.Context, roomID domain.RoomID, send func() error) error {
	b.sendMu.RLock()
	defer b.sendMu.RUnlock()
	if b.client.Crypto == nil && b.encryptedWithoutCrypto(ctx, roomID) {
		return fmt.Errorf("matrix: %s is encrypted: %w", roomID, api.ErrNoEncryption)
	}
	return send()
}

// encryptedWithoutCrypto asks whether a room is encrypted when there may be no state
// store (the crypto machine brings it): the homeserver's answer then, failing closed.
func (b *InProc) encryptedWithoutCrypto(ctx context.Context, roomID domain.RoomID) bool {
	if b.stateStore() != nil {
		return b.roomEncrypted(ctx, roomID)
	}
	var content event.EncryptionEventContent
	err := b.client.StateEvent(ctx, id.RoomID(roomID), event.StateEncryption, "", &content)
	if errors.Is(err, mautrix.MNotFound) {
		return false
	}
	b.warnIf(ctx, err, "read room encryption state; treating it as encrypted", "room", roomID)
	return true
}

// beginStopping sets stopping under dispatchMu, so a late decryption already past
// its check (see postDecrypt) finishes first, and every later one drops its event.
func (b *InProc) beginStopping() {
	b.dispatchMu.Lock()
	defer b.dispatchMu.Unlock()
	b.stopping.Store(true)
}

// postDecrypt hands a decrypted event to the sync handlers, as mautrix would, unless
// Stop has begun: a retry that waited for its keys can finish after the stores close.
// The check and the dispatch hold dispatchMu, so Stop, which takes it for write, waits
// for a dispatch that got past the check before closing the stores.
func (b *InProc) postDecrypt(ctx context.Context, evt *event.Event) {
	b.dispatchMu.RLock()
	defer b.dispatchMu.RUnlock()
	if b.stopping.Load() {
		return
	}
	if syncer, ok := b.client.Syncer.(mautrix.DispatchableSyncer); ok {
		syncer.Dispatch(ctx, evt)
	}
}

// stateStore is the client's state store, nil when OpenCryptoStore did not run or
// failed. It is written once, before any RPC is served, so it needs no lock.
func (b *InProc) stateStore() mautrix.StateStore {
	return b.client.StateStore
}

// publishCrypto makes the helper visible to every other goroutine.
func (b *InProc) publishCrypto(helper *cryptohelper.CryptoHelper) {
	b.machineMu.Lock()
	defer b.machineMu.Unlock()
	b.crypto = helper
}

// cryptoHelper is the crypto machine, or nil before EnableEncryption has published one.
func (b *InProc) cryptoHelper() *cryptohelper.CryptoHelper {
	b.machineMu.RLock()
	defer b.machineMu.RUnlock()
	return b.crypto
}
