package api

import "errors"

// Sentinel errors are part of the Backend contract. They live here, not in
// internal/matrix, so the daemon transport can preserve their identity across the
// socket without pulling the Matrix SDK into the client.
var (
	// ErrNoSpaceParent: the room was filed into (or out of) a space, but the room
	// refused the matching m.space.parent. A partial success, not a failure.
	ErrNoSpaceParent = errors.New("matrix: the room refused to record the space")
	// ErrNoPower: the account lacks the power level the room demands. Usually
	// raised before the request so the message can name the levels involved.
	ErrNoPower = errors.New("matrix: not enough power in this room")
	// ErrNoEncryption: the operation needs the crypto machine and E2EE is off.
	ErrNoEncryption = errors.New("matrix: encryption is not enabled")
	// ErrNoKeyBackup: the account has no server-side room-key backup.
	ErrNoKeyBackup = errors.New("matrix: no key backup on the server to restore")
	// ErrBadRecoveryKey: the recovery key or passphrase does not unlock secret storage.
	ErrBadRecoveryKey = errors.New("matrix: recovery key or passphrase is incorrect")
	// ErrBadKeyFile: a key export cannot be read. A wrong passphrase and a corrupt
	// file fail the same MAC check, so they are deliberately one error.
	ErrBadKeyFile = errors.New("matrix: not a readable key export (wrong passphrase, or not an export)")
	// ErrNoRoomKeys: a key export was asked for and this device holds no room keys.
	ErrNoRoomKeys = errors.New("matrix: this device holds no room keys to export")
	// ErrKeyBackupExists: bootstrapping would overwrite existing secret storage or a
	// key backup — irreversible, so it refuses.
	ErrKeyBackupExists = errors.New("matrix: this account already has secret storage or a key backup")
	// ErrSessionRejected: the homeserver rejected the saved token. The only case
	// where logging in again (which mints a new, unverified device) is right.
	ErrSessionRejected = errors.New("matrix: the homeserver rejected the saved session")
	// ErrUnreachable: the homeserver could not be reached; says nothing about the session.
	ErrUnreachable = errors.New("matrix: cannot reach the homeserver")
	// ErrBadPassword: the homeserver rejected the account password during UIA, or
	// Telegram the account's two-step verification password.
	ErrBadPassword = errors.New("the account password is not correct")
	// ErrEditsRemain: the message was deleted but some earlier versions (separate
	// m.replace events) could not be. The deletion itself succeeded.
	ErrEditsRemain = errors.New("matrix: the message is deleted, but earlier versions of it could not be removed")
	// ErrSpellUnavailable: no spelling engine or dictionary. A state, not an
	// incident — callers should stop asking.
	ErrSpellUnavailable = errors.New("matrix: nothing to check spelling with")
	// ErrSeatTaken: another window has the seat (Seat): a window asking for it
	// without force is refused, and a window that lost it has its draft writes refused.
	ErrSeatTaken = errors.New("daemon: kith is open in another window")
	// ErrNetworkOff: the room is on a network this daemon has no connection to (its
	// account was removed from the config, or never added). Its cached history stays
	// readable.
	ErrNetworkOff = errors.New("daemon: that room's network is not connected")
	// ErrNotOnNetwork: the room's network has no such thing (spaces, threads,
	// moderation are Matrix's).
	ErrNotOnNetwork = errors.New("daemon: that room's network cannot do this")
)
