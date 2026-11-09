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
	MessagesPerRoom *int `toml:"messages_per_room"`
	// ArchiveMedia is whether kithd keeps a copy of every attachment (nil: on). Rules
	// override it per place.
	ArchiveMedia *bool `toml:"archive_media"`
	// ArchiveWarnMB is how large the copies grow before kithd warns; it never deletes
	// them. 0 is DefaultArchiveWarnMB, negative never warns.
	ArchiveWarnMB int           `toml:"archive_warn_mb"`
	Rules         []StorageRule `toml:"rule"`
}

// DefaultArchiveWarnMB is archive_warn_mb when unset: 10 GiB.
const DefaultArchiveWarnMB = 10 << 10

// StorageRule is one [[storage.rule]]: what one place keeps instead.
type StorageRule struct {
	// Match names the place as every place list does: a room ID, room:<name>,
	// space:<name>, tag:<name>, place:<name>, protocol:<network>, dm or group.
	Match    string `toml:"match"`
	Messages *int   `toml:"messages"` // negative keeps every one
	// ArchiveMedia is archive_media for this place; nil inherits.
	ArchiveMedia *bool `toml:"archive_media"`
}

// MessagesKept is messages_per_room: negative (every one) when unset.
func (s Storage) MessagesKept() int {
	if s.MessagesPerRoom == nil {
		return -1
	}
	return *s.MessagesPerRoom
}

// MediaArchived is archive_media: on when unset.
func (s Storage) MediaArchived() bool { return enabled(s.ArchiveMedia) }

// ArchiveWarnBytes is archive_warn_mb in bytes; negative never warns.
func (s Storage) ArchiveWarnBytes() int64 {
	switch {
	case s.ArchiveWarnMB < 0:
		return -1
	case s.ArchiveWarnMB == 0:
		return DefaultArchiveWarnMB << 20
	}
	return int64(s.ArchiveWarnMB) << 20
}
