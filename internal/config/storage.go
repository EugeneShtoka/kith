package config

// Storage is [storage]: where kith keeps its files. Empty directories are the XDG
// defaults; an empty instance is chosen and written on first run.
type Storage struct {
	// Instance names this config's files in the directories below, so several
	// configs can share them. Not tied to any account.
	Instance       string `toml:"instance"`
	DataDir        string `toml:"data_dir"`
	StateDir       string `toml:"state_dir"`
	CacheDir       string `toml:"cache_dir"`
	RuntimeDir     string `toml:"runtime_dir"`
	ExportDir      string `toml:"export_dir"` // where /export writes; empty is <data_dir>/exports
	KeyringService string `toml:"keyring_service"`
	// MessagesPerRoom is how many newest messages a room keeps; nil or negative keeps
	// all. Rules override it per place.
	MessagesPerRoom *int          `toml:"messages_per_room"`
	Rules           []StorageRule `toml:"rule"`
}

// StorageRule is one [[storage.rule]]: what one place keeps instead.
type StorageRule struct {
	// Match names the place as every place list does: a room ID, room:<name>,
	// space:<name>, tag:<name>, place:<name>, protocol:<network>, dm or group.
	Match    string `toml:"match"`
	Messages *int   `toml:"messages"` // negative keeps every one
}

// MessagesKept is messages_per_room: negative (every one) when unset.
func (s Storage) MessagesKept() int {
	if s.MessagesPerRoom == nil {
		return -1
	}
	return *s.MessagesPerRoom
}
