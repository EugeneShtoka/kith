package domain

import "path/filepath"

// Storage is where one kith instance keeps everything: the configured directories,
// the instance ID that names its files in them (so several instances can share a
// directory), and the keyring service its secrets are kept under. Nothing in it
// depends on any network account.
type Storage struct {
	Instance       string
	DataDir        string // stores: cache, crypto, WhatsApp; dictionaries, models
	StateDir       string // session file, agent ledger, send-later queue, logs
	CacheDir       string // media
	RuntimeDir     string // socket and lock
	ExportDir      string // where /export writes, when given no folder
	KeyringService string
}

// CachePath is the local cache.
func (s Storage) CachePath() string { return s.store("cache") }

// CryptoPath is the Matrix crypto and state store.
func (s Storage) CryptoPath() string { return s.store("crypto") }

// WhatsAppPath is the WhatsApp session store (every linked account's device keys).
func (s Storage) WhatsAppPath() string { return s.store("whatsapp") }

// TelegramPath is the Telegram store: the accounts' updates positions and access
// hashes (the sessions are in the keyring).
func (s Storage) TelegramPath() string { return s.store("telegram") }

func (s Storage) store(kind string) string {
	return filepath.Join(s.DataDir, kind+"-"+s.Instance+".db")
}

// SessionFile is the session's fallback file, when the keyring cannot hold it.
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

// SocketPath is the daemon's socket.
func (s Storage) SocketPath() string { return filepath.Join(s.RuntimeDir, s.Instance+".sock") }

// LockPath is the daemon's single-instance lock, beside its socket.
func (s Storage) LockPath() string { return filepath.Join(s.RuntimeDir, s.Instance+".lock") }

// LogPath is where a program of this instance logs when it logs to a file.
func (s Storage) LogPath(program string) string { return filepath.Join(s.StateDir, program+".log") }

// MediaDir is the client's picture cache.
func (s Storage) MediaDir() string { return filepath.Join(s.CacheDir, "media") }
