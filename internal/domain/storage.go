package domain

import "path/filepath"

// Storage is where one kith instance keeps everything. Instance names its files, so
// several instances can share a directory. Nothing in it depends on a network account.
type Storage struct {
	Instance       string
	DataDir        string // stores: cache, crypto, WhatsApp; dictionaries, models
	StateDir       string // session file, agent ledger, send-later queue, logs
	CacheDir       string // media
	RuntimeDir     string // socket and lock
	ExportDir      string // where /export writes, when given no folder
	Backups        string // where kithd's scheduled backups go; empty is <ExportDir>/backups
	KeyringService string
}

// CachePath is the local cache.
func (s Storage) CachePath() string { return s.store("cache") }

// CryptoPath is the Matrix crypto and state store an instance kept before Matrix
// accounts (MatrixCryptoPath): what tells an older install's files apart.
func (s Storage) CryptoPath() string { return s.store("crypto") }

// MatrixCryptoPath is the crypto and state store of the Matrix account whose key
// (AccountKey of its Matrix ID) is key.
func (s Storage) MatrixCryptoPath(key string) string {
	return filepath.Join(s.DataDir, "crypto-"+s.Instance+"-"+key+".db")
}

// MatrixSessionFile is the session's fallback file of the Matrix account whose key is
// key, when the keyring cannot hold it.
func (s Storage) MatrixSessionFile(key string) string {
	return filepath.Join(s.StateDir, "session-"+s.Instance+"-"+key+".toml")
}

// WhatsAppPath is the WhatsApp session store (every linked account's device keys).
func (s Storage) WhatsAppPath() string { return s.store("whatsapp") }

// TelegramPath is the Telegram store: the accounts' updates positions and access
// hashes (the sessions are in the keyring).
func (s Storage) TelegramPath() string { return s.store("telegram") }

// MessengerPath is the Messenger store: the devices that read Messenger's end-to-end
// encrypted chats (the sessions are in the keyring).
func (s Storage) MessengerPath() string { return s.store("messenger") }

// SignalPath is the Signal key store (every linked account's device keys), which only
// kith-signal opens.
func (s Storage) SignalPath() string { return s.store("signal") }

// SignalSocketPath is the socket kithd serves kith-signal through (kithd listens; the
// helper is handed it).
func (s Storage) SignalSocketPath() string {
	return filepath.Join(s.RuntimeDir, "signal-"+s.Instance+".sock")
}

func (s Storage) store(kind string) string {
	return filepath.Join(s.DataDir, kind+"-"+s.Instance+".db")
}

// SessionFile is the session's fallback file an instance kept before Matrix accounts
// (MatrixSessionFile).
func (s Storage) SessionFile() string {
	return filepath.Join(s.StateDir, "session-"+s.Instance+".toml")
}

// LedgerPath is the record of what an assistant wrote through kith-mcp.
func (s Storage) LedgerPath() string {
	return filepath.Join(s.StateDir, "agent-sends-"+s.Instance+".jsonl")
}

// AgentFilesDir is where kith-mcp puts the attachments an assistant asks for.
func (s Storage) AgentFilesDir() string {
	return filepath.Join(s.CacheDir, "agent-files-"+s.Instance)
}

// SchedulePath is the send-later queue.
func (s Storage) SchedulePath() string {
	return filepath.Join(s.StateDir, "scheduled-"+s.Instance+".toml")
}

// BackupKeyPath is the public key backups are encrypted to. It is not secret: it can
// only lock a backup, and the private key that opens one is the user's, never kept
// here. It sits beside the cache, which a restore recreates, rather than with the
// config, which a home-manager setup may own.
func (s Storage) BackupKeyPath() string {
	return filepath.Join(s.DataDir, "backup-"+s.Instance+".pub")
}

// BackupsDir is where kithd keeps its scheduled backups.
func (s Storage) BackupsDir() string {
	if s.Backups != "" {
		return s.Backups
	}
	return filepath.Join(s.ExportDir, "backups")
}

// DevicesPath is the roster of devices paired with kithd as a file: rendered from the
// mesh store (MeshPath), and read back into it after a hand edit.
func (s Storage) DevicesPath() string {
	return filepath.Join(s.DataDir, "devices-"+s.Instance+".toml")
}

// MeshPath is the mesh store: the roster as signed ops (internal/mesh), sealed under
// the cache's key. Not the cache: a rebuild must not unpair anyone.
func (s Storage) MeshPath() string {
	return filepath.Join(s.DataDir, "mesh-"+s.Instance+".db")
}

// DeviceKeyFile is the fallback file of the device key named name ("device" is
// kithd's, "client-device" this machine's as a client), when the keyring cannot hold
// it.
func (s Storage) DeviceKeyFile(name string) string {
	return filepath.Join(s.StateDir, name+"-"+s.Instance+".key")
}

// SocketPath is the daemon's socket.
func (s Storage) SocketPath() string { return filepath.Join(s.RuntimeDir, s.Instance+".sock") }

// LockPath is the daemon's single-instance lock, beside its socket.
func (s Storage) LockPath() string { return filepath.Join(s.RuntimeDir, s.Instance+".lock") }

// NotifierLockPath is kith-notify's single-instance lock: one shows the instance's
// notifications on this machine.
func (s Storage) NotifierLockPath() string {
	return filepath.Join(s.RuntimeDir, s.Instance+"-notify.lock")
}

// LogPath is where a program of this instance logs when it logs to a file.
func (s Storage) LogPath(program string) string { return filepath.Join(s.StateDir, program+".log") }

// MediaDir is the client's picture cache.
func (s Storage) MediaDir() string { return filepath.Join(s.CacheDir, "media") }

// UploadsDir is where kithd keeps the files clients send it until each is posted.
func (s Storage) UploadsDir() string {
	return filepath.Join(s.CacheDir, "uploads-"+s.Instance)
}
