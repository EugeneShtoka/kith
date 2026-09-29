package matrix

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
)

// gossipTimeout bounds how long restoreFromGossip waits for a verified device to
// answer a secret request for the backup key.
const gossipTimeout = 30 * time.Second

// RestoreKeyBackup unlocks secret storage with a recovery key or passphrase, imports
// every room key from the server-side backup, and (with a recovery key) self-signs
// this device. secret is never persisted. It returns the backup's key count.
func (b *InProc) RestoreKeyBackup(ctx context.Context, secret string) (int, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return 0, api.ErrNoEncryption
	}
	mach := helper.Machine()

	keyID, keyData, err := mach.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return 0, fmt.Errorf("matrix: read secret storage (is key backup set up?): %w", err)
	}

	ssssKey, viaRecoveryKey, err := unlockSSSS(keyID, keyData, secret)
	if err != nil {
		return 0, err
	}

	backupKey, err := megolmBackupKey(ctx, mach.SSSS, ssssKey)
	if err != nil {
		return 0, err
	}

	count, err := importKeyBackup(ctx, mach, backupKey)
	if err != nil {
		return 0, err
	}

	// Best-effort cross-signing (recovery key only); must not undo the import.
	if viaRecoveryKey {
		b.warnIf(ctx, mach.VerifyWithRecoveryKey(ctx, secret), "cross-sign with the recovery key")
	}

	return count, nil
}

// restoreFromGossip requests the backup key from a verified device and imports the
// backup; run after SAS verification. ErrNoKeyBackup when nobody answered.
func (b *InProc) restoreFromGossip(ctx context.Context) (int, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return 0, api.ErrNoEncryption
	}
	mach := helper.Machine()

	var backupKey *backup.MegolmBackupKey
	err := mach.GetOrRequestSecret(ctx, id.SecretMegolmBackupV1, func(secret string) (bool, error) {
		// (false, nil) means "not it, keep waiting".
		key, ok := parseBackupKey(secret)
		if !ok {
			return false, nil
		}
		backupKey = key
		return true, nil
	}, gossipTimeout)
	if err != nil {
		return 0, fmt.Errorf("matrix: request backup key: %w", err)
	}
	if backupKey == nil {
		return 0, api.ErrNoKeyBackup
	}
	return importKeyBackup(ctx, mach, backupKey)
}

// importKeyBackup verifies the latest backup version against the key and imports it,
// returning the version's key count.
func importKeyBackup(ctx context.Context, mach *crypto.OlmMachine, backupKey *backup.MegolmBackupKey) (int, error) {
	version, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, backupKey)
	if err != nil {
		return 0, fmt.Errorf("matrix: verify key backup: %w", err)
	}
	if version == nil {
		return 0, api.ErrNoKeyBackup
	}
	if err := mach.GetAndStoreKeyBackup(ctx, version.Version, backupKey); err != nil {
		return 0, fmt.Errorf("matrix: import key backup: %w", err)
	}
	return version.Count, nil
}

// unlockSSSS resolves the SSSS key from a recovery key or passphrase, reporting
// whether it was the recovery key.
func unlockSSSS(keyID string, keyData *ssss.KeyMetadata, secret string) (*ssss.Key, bool, error) {
	if key, err := keyData.VerifyRecoveryKey(keyID, secret); err == nil {
		return key, true, nil
	}
	if keyData.Passphrase != nil {
		if key, err := keyData.VerifyPassphrase(keyID, secret); err == nil {
			return key, false, nil
		}
	}
	return nil, false, api.ErrBadRecoveryKey
}

// megolmBackupKey reads and decrypts the megolm backup private key from SSSS.
func megolmBackupKey(ctx context.Context, sm *ssss.Machine, key *ssss.Key) (*backup.MegolmBackupKey, error) {
	raw, err := sm.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key)
	if err != nil {
		return nil, fmt.Errorf("matrix: read backup key from secret storage: %w", err)
	}
	backupKey, ok := parseBackupKey(string(raw))
	if !ok {
		return nil, errors.New("matrix: stored backup key is malformed")
	}
	return backupKey, nil
}

// parseBackupKey parses a base64 backup secret; ok is false when unusable.
func parseBackupKey(secret string) (*backup.MegolmBackupKey, bool) {
	decoded, err := decodeBackupSecret(secret)
	if err != nil {
		return nil, false
	}
	key, err := backup.MegolmBackupKeyFromBytes(decoded)
	if err != nil {
		return nil, false
	}
	return key, true
}

// decodeBackupSecret decodes the base64 SSSS secret, tolerating both padded and
// unpadded standard base64 (clients differ).
func decodeBackupSecret(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil {
		return decoded, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("matrix: base64: %w", err)
	}
	return decoded, nil
}
