package domain

// KeyBackup is the result of bootstrapping a server-side room-key backup.
type KeyBackup struct {
	// RecoveryKey is the base58 secret that unlocks secret storage, and so the backup.
	RecoveryKey string
	// Version identifies the backup on the homeserver.
	Version string
	// Uploaded is how many of this device's room keys reached the new backup.
	Uploaded int
	// Incomplete says why the backup was not finished; empty when it was.
	Incomplete string
}
