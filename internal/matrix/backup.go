package matrix

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Uploading room keys into the server-side backup (keybackup.go restores; mautrix
// v0.30 never uploads). No secret is needed: sessions are encrypted to the backup's
// public key. The version must be verified first (GetAndVerifyLatestKeyBackupVersion),
// since an attacker's backup key would receive every room key.

const (
	// backupBatch is how many sessions go in one PUT.
	backupBatch = 100
	// backupPass bounds sessions read per pass: reads must finish before marking
	// (the crypto store has one connection; writing during a query deadlocks).
	backupPass = 1000
)

// BackupRoomKeys uploads every room key not yet in the account's backup and returns
// how many. Incremental: sessions record the version they reached, so the daemon's
// periodic sweep is cheap. ErrNoEncryption / ErrNoKeyBackup mean nothing to do.
func (b *InProc) BackupRoomKeys(ctx context.Context) (int, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return 0, api.ErrNoEncryption
	}
	mach := helper.Machine()
	// No room keys (common on unencrypted accounts): skip the homeserver round trip.
	held, err := holdsRoomKeys(ctx, mach.CryptoStore)
	if err != nil || !held {
		return 0, err
	}
	version, pub, err := trustedBackup(ctx, mach)
	if err != nil {
		return 0, err
	}
	return uploadRoomKeys(ctx, mach.Client, mach.CryptoStore, version, pub)
}

// trustedBackup resolves the latest backup version and its public key, refusing one
// this device cannot verify. The nil key forces the signature check (master
// cross-signing key or a trusted device) rather than a match against a held key.
func trustedBackup(ctx context.Context, mach *crypto.OlmMachine) (id.KeyBackupVersion, *ecdh.PublicKey, error) {
	version, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, nil)
	switch {
	case errors.Is(err, mautrix.MNotFound):
		return "", nil, api.ErrNoKeyBackup
	case err != nil:
		return "", nil, fmt.Errorf("matrix: verify key backup: %w", err)
	case version == nil:
		return "", nil, api.ErrNoKeyBackup
	}
	pub, err := backupPublicKey(version.AuthData)
	if err != nil {
		return "", nil, err
	}
	return version.Version, pub, nil
}

// backupPublicKey decodes the curve25519 public key a backup version publishes.
func backupPublicKey(authData backup.MegolmAuthData) (*ecdh.PublicKey, error) {
	// Accept padded and unpadded base64 (clients differ).
	raw, err := decodeBackupSecret(string(authData.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("matrix: key backup public key: %w", err)
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("matrix: key backup public key: %w", err)
	}
	return pub, nil
}

// uploadRoomKeys uploads every session not yet in version, in batches, and returns
// how many landed (also on a part-way failure).
func uploadRoomKeys(
	ctx context.Context,
	client *mautrix.Client,
	store crypto.Store,
	version id.KeyBackupVersion,
	pub *ecdh.PublicKey,
) (int, error) {
	uploaded := 0
	var previous id.SessionID
	for {
		pending, err := pendingSessions(ctx, store, version, backupPass)
		if err != nil {
			return uploaded, err
		}
		if len(pending) == 0 {
			return uploaded, nil
		}
		// The same first session twice means marking failed; stop rather than loop forever.
		first := pending[0].ID()
		if first == previous {
			return uploaded, fmt.Errorf(
				"matrix: room key %s is still outside backup %s after being uploaded", first, version)
		}
		previous = first
		for batch := range slices.Chunk(pending, backupBatch) {
			if err := uploadBatch(ctx, client, store, version, pub, batch); err != nil {
				return uploaded, err
			}
			uploaded += len(batch)
		}
	}
}

// uploadBatch encrypts, PUTs, then marks one batch (marking only after the server holds it).
func uploadBatch(
	ctx context.Context,
	client *mautrix.Client,
	store crypto.Store,
	version id.KeyBackupVersion,
	pub *ecdh.PublicKey,
	batch []*crypto.InboundGroupSession,
) error {
	req, err := backupRequest(batch, pub)
	if err != nil {
		return err
	}
	if _, err := client.PutKeysInBackup(ctx, version, req); err != nil {
		return fmt.Errorf("matrix: upload room keys: %w", err)
	}
	return markBackedUp(ctx, store, batch, version)
}

// backupRequest builds one PUT body, sessions encrypted and grouped by room.
func backupRequest(sessions []*crypto.InboundGroupSession, pub *ecdh.PublicKey) (*mautrix.ReqKeyBackup, error) {
	req := &mautrix.ReqKeyBackup{Rooms: make(map[id.RoomID]mautrix.ReqRoomKeyBackup, len(sessions))}
	for _, session := range sessions {
		payload, err := sessionPayload(session)
		if err != nil {
			return nil, err
		}
		encrypted, err := backup.EncryptSessionDataWithPubkey(pub, payload)
		if err != nil {
			return nil, fmt.Errorf("matrix: encrypt room key for backup: %w", err)
		}
		data, err := json.Marshal(encrypted)
		if err != nil {
			return nil, fmt.Errorf("matrix: encode room key for backup: %w", err)
		}
		if _, ok := req.Rooms[session.RoomID]; !ok {
			req.Rooms[session.RoomID] = mautrix.ReqRoomKeyBackup{
				Sessions: map[id.SessionID]mautrix.ReqKeyBackupData{},
			}
		}
		req.Rooms[session.RoomID].Sessions[session.ID()] = mautrix.ReqKeyBackupData{
			FirstMessageIndex: int(session.Internal.FirstKnownIndex()),
			ForwardedCount:    len(session.ForwardingChains),
			// We do not record whether we verified the sender device, so never claim it.
			IsVerified:  false,
			SessionData: data,
		}
	}
	return req, nil
}

// sessionPayload is the backup plaintext: the megolm key at its earliest known index
// plus attribution fields — the same fields mautrix's importer reads back (its own
// exporter is unexported).
func sessionPayload(session *crypto.InboundGroupSession) (backup.MegolmSessionData, error) {
	key, err := session.Internal.Export(session.Internal.FirstKnownIndex())
	if err != nil {
		return backup.MegolmSessionData{}, fmt.Errorf("matrix: export room key %s: %w", session.ID(), err)
	}
	return backup.MegolmSessionData{
		Algorithm:          id.AlgorithmMegolmV1,
		ForwardingKeyChain: session.ForwardingChains,
		SenderClaimedKeys:  backup.SenderClaimedKeys{Ed25519: session.SigningKey},
		SenderKey:          session.SenderKey,
		SessionKey:         string(key),
		SharedHistory:      session.SharedHistory,
	}, nil
}

// backupMarker is the SQL crypto store's one-column "session is in backup N" update;
// other stores fall back to a full upsert.
type backupMarker interface {
	SetGroupSessionKeyBackupVersion(ctx context.Context, sessionID id.SessionID, version id.KeyBackupVersion) error
}

// markBackedUp records that sessions reached version (after the PUT, never before).
func markBackedUp(
	ctx context.Context,
	store crypto.Store,
	sessions []*crypto.InboundGroupSession,
	version id.KeyBackupVersion,
) error {
	if marker, ok := store.(backupMarker); ok {
		for _, session := range sessions {
			if err := marker.SetGroupSessionKeyBackupVersion(ctx, session.ID(), version); err != nil {
				return fmt.Errorf("matrix: record room key %s as backed up: %w", session.ID(), err)
			}
		}
		return nil
	}
	for _, session := range sessions {
		session.KeyBackupVersion = version
		if err := store.PutGroupSession(ctx, session); err != nil {
			return fmt.Errorf("matrix: record room key %s as backed up: %w", session.ID(), err)
		}
	}
	return nil
}

// pendingSessions reads up to limit sessions not yet in version, fully, before
// returning (see backupPass).
func pendingSessions(
	ctx context.Context,
	store crypto.Store,
	version id.KeyBackupVersion,
	limit int,
) ([]*crypto.InboundGroupSession, error) {
	var pending []*crypto.InboundGroupSession
	err := store.GetGroupSessionsWithoutKeyBackupVersion(ctx, version).Iter(
		func(session *crypto.InboundGroupSession) (bool, error) {
			pending = append(pending, session)
			return len(pending) < limit, nil
		})
	if err != nil {
		return nil, fmt.Errorf("matrix: read room keys awaiting backup: %w", err)
	}
	return pending, nil
}

// holdsRoomKeys reports whether this device has any room key at all.
func holdsRoomKeys(ctx context.Context, store crypto.Store) (bool, error) {
	held := false
	err := store.GetAllGroupSessions(ctx).Iter(func(*crypto.InboundGroupSession) (bool, error) {
		held = true
		return false, nil
	})
	if err != nil {
		return false, fmt.Errorf("matrix: read room keys: %w", err)
	}
	return held, nil
}
