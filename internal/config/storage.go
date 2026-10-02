package config

// Storage is [storage]: where this config's kith keeps its files, and under what
// name. Empty directories are the XDG defaults; an empty instance is chosen (and
// written here) the first time kith runs.
type Storage struct {
	// Instance names this config's files in the directories below, so several
	// configs can share them. Not tied to any account.
	Instance       string `toml:"instance"`
	DataDir        string `toml:"data_dir"`
	StateDir       string `toml:"state_dir"`
	CacheDir       string `toml:"cache_dir"`
	RuntimeDir     string `toml:"runtime_dir"`
	KeyringService string `toml:"keyring_service"`
}
