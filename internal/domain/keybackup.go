package domain

// KeyBackup is what bootstrapping a server-side room-key backup produced: the secret
// the user has to keep, the version it created, and how much of this device's key store
// made it into that version before the call returned.
type KeyBackup struct {
	// RecoveryKey is the base58 secret that unlocks the account's secret storage, and
	// therefore everything in the backup.
	RecoveryKey string
	// Version identifies the backup on the homeserver.
	Version string
	// Uploaded is how many of this device's room keys reached the new backup.
	Uploaded int
	// Incomplete says why the backup was not finished, and is empty when it was.
	Incomplete string
}
