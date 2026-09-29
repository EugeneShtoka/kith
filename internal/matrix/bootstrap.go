package matrix

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Bootstrapping key backup on an account with none. Dangerous: it mints a new
// cross-signing identity and recovery key, so it refuses (refuseIfSetUp) rather
// than overwrite an existing setup.

// BootstrapKeyBackup creates cross-signing keys, secret storage with a fresh recovery
// key, a backup version whose key lives there, and a first upload. password is the
// account password (publishing cross-signing keys needs user-interactive auth).
func (b *InProc) BootstrapKeyBackup(ctx context.Context, password string) (domain.KeyBackup, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return domain.KeyBackup{}, api.ErrNoEncryption
	}
	mach := helper.Machine()
	if err := refuseIfSetUp(ctx, mach); err != nil {
		return domain.KeyBackup{}, err
	}

	recoveryKey, keys, err := mach.GenerateAndUploadCrossSigningKeysWithPassword(ctx, password, "")
	if err != nil {
		if errors.Is(err, mautrix.MForbidden) {
			return domain.KeyBackup{}, api.ErrBadPassword
		}
		return domain.KeyBackup{}, fmt.Errorf("matrix: set up cross-signing: %w", err)
	}
	// From here the recovery key exists only in this value: later failures go in
	// Incomplete rather than an error that would discard it.
	made := domain.KeyBackup{RecoveryKey: recoveryKey}

	// Best-effort self-signing so other clients see this device as verified.
	b.warnIf(ctx, mach.SignOwnDevice(ctx, mach.OwnIdentity()), "sign own device")
	b.warnIf(ctx, mach.SignOwnMasterKey(ctx), "sign own master key")

	version, backupKey, err := createBackupVersion(ctx, mach, keys.MasterKey, recoveryKey)
	if err != nil {
		made.Incomplete = err.Error()
		return made, nil //nolint:nilerr // it is in Incomplete; an error would discard the recovery key
	}
	made.Version = string(version)

	uploaded, err := uploadRoomKeys(ctx, mach.Client, mach.CryptoStore, version, backupKey.PublicKey())
	made.Uploaded = uploaded
	if err != nil {
		// Not fatal: the daemon's sweep continues the upload.
		made.Incomplete = err.Error()
	}
	return made, nil
}

// refuseIfSetUp returns ErrKeyBackupExists when secret storage or a backup version
// exists (either alone is enough). An unclear answer also stops the bootstrap.
func refuseIfSetUp(ctx context.Context, mach *crypto.OlmMachine) error {
	switch _, err := mach.SSSS.GetDefaultKeyID(ctx); {
	case err == nil:
		return api.ErrKeyBackupExists
	case errors.Is(err, ssss.ErrNoDefaultKeyID):
		// No secret storage yet, which is what we are here to create.
	default:
		return fmt.Errorf("matrix: read secret storage: %w", err)
	}
	switch _, err := mach.Client.GetKeyBackupLatestVersion(ctx); {
	case err == nil:
		return api.ErrKeyBackupExists
	case errors.Is(err, mautrix.MNotFound):
		// No backup version yet.
	default:
		return fmt.Errorf("matrix: read key backup version: %w", err)
	}
	return nil
}

// createBackupVersion generates a backup key, publishes a version signed by the
// master key and stores the private key in secret storage. If storing fails the
// version is deleted: a trusted version nobody can decrypt is the worst outcome.
func createBackupVersion(
	ctx context.Context,
	mach *crypto.OlmMachine,
	master olm.PKSigning,
	recoveryKey string,
) (id.KeyBackupVersion, *backup.MegolmBackupKey, error) {
	backupKey, err := backup.NewMegolmBackupKey()
	if err != nil {
		return "", nil, fmt.Errorf("matrix: generate key backup key: %w", err)
	}
	authData, err := signedAuthData(mach.Client.UserID, master, backupKey)
	if err != nil {
		return "", nil, err
	}
	created, err := mach.Client.CreateKeyBackupVersion(ctx, &mautrix.ReqRoomKeysVersionCreate[backup.MegolmAuthData]{
		Algorithm: id.KeyBackupAlgorithmMegolmBackupV1,
		AuthData:  authData,
	})
	if err != nil {
		return "", nil, fmt.Errorf("matrix: create key backup version: %w", err)
	}
	if err := storeBackupKey(ctx, mach, backupKey, recoveryKey); err != nil {
		// Undo the half-made version; if that fails too, say so, or an orphan backup
		// nobody holds the key to is left on the server unexplained.
		if derr := mach.Client.DeleteKeyBackupVersion(ctx, created.Version); derr != nil {
			err = errors.Join(err, fmt.Errorf("matrix: remove the unusable key backup version %s: %w", created.Version, derr))
		}
		return "", nil, err
	}
	// Keep the crypto store's record in step with the server.
	if err := mach.SetKeyBackupVersion(ctx, created.Version); err != nil {
		return "", nil, fmt.Errorf("matrix: record key backup version: %w", err)
	}
	return created.Version, backupKey, nil
}

// signedAuthData builds a backup version's auth_data: the public key, signed by the
// master cross-signing key so clients without the private key can trust it.
func signedAuthData(
	user id.UserID,
	master olm.PKSigning,
	key *backup.MegolmBackupKey,
) (backup.MegolmAuthData, error) {
	// mautrix types this curve25519 key as Ed25519; it is unpadded base64 either way.
	authData := backup.MegolmAuthData{
		PublicKey: id.Ed25519(base64.RawStdEncoding.EncodeToString(key.PublicKey().Bytes())),
	}
	// SignJSON ignores "signatures", so sign first and fill it in after.
	signature, err := master.SignJSON(authData)
	if err != nil {
		return backup.MegolmAuthData{}, fmt.Errorf("matrix: sign key backup: %w", err)
	}
	authData.Signatures = signatures.NewSingleSignature(
		user, id.KeyAlgorithmEd25519, master.PublicKey().String(), signature)
	return authData, nil
}

// storeBackupKey puts the backup's private key into secret storage (what
// RestoreKeyBackup reads).
func storeBackupKey(
	ctx context.Context,
	mach *crypto.OlmMachine,
	key *backup.MegolmBackupKey,
	recoveryKey string,
) error {
	keyID, keyData, err := mach.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return fmt.Errorf("matrix: read the secret storage just created: %w", err)
	}
	ssssKey, _, err := unlockSSSS(keyID, keyData, recoveryKey)
	if err != nil {
		// Our own fresh key failing means the storage is wrong, not the key.
		return fmt.Errorf("matrix: the secret storage just created does not accept its own recovery key: %w", err)
	}
	// Padded base64, as other clients write it.
	secret := []byte(base64.StdEncoding.EncodeToString(key.Bytes()))
	if err := mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataMegolmBackupKey, secret, ssssKey); err != nil {
		return fmt.Errorf("matrix: store the backup key in secret storage: %w", err)
	}
	return nil
}
