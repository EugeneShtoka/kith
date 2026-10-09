package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// defaultKeyring is the keyring service when [storage] names none.
const defaultKeyring = "kith"

// validInstance is what an instance may be: it is part of file names.
var validInstance = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// StorageFor is where the config at configPath keeps its files, for the profile
// chosen (cfg is that profile's, as Config.Profile returns it). It never writes the
// config — the daemon runs with it read-only — so an instance the config does not
// name is derived, the same in every program: the name an install from before kept
// its files under, when they exist, else one from the config file's path.
// RememberInstance writes it into the config.
func StorageFor(cfg config.Config, configPath, profile string) (domain.Storage, error) {
	storage, err := StorageDirs(cfg)
	if err != nil {
		return domain.Storage{}, err
	}
	base, err := baseInstance(cfg, configPath, storage)
	if err != nil {
		return domain.Storage{}, err
	}
	storage.Instance = base
	if name := chosenProfile(cfg, profile); name != "" {
		storage.Instance = base + "-" + name
		// A profile kept its files under its own account's name before instances.
		if legacy := domain.AccountKey(cfg.User); cfg.User != "" && hasFiles(storage, legacy) {
			storage.Instance = legacy
		}
	}
	return storage, nil
}

// StorageDirs is the config's directories and keyring service, without an instance:
// enough for what every instance shares (dictionaries, models, API keys), and never
// writing the config. A directory must be absolute (or start with "~").
func StorageDirs(cfg config.Config) (domain.Storage, error) {
	s := cfg.Storage
	storage := domain.Storage{
		DataDir:        dirOr(s.DataDir, xdg.DataHome),
		StateDir:       dirOr(s.StateDir, xdg.StateHome),
		CacheDir:       dirOr(s.CacheDir, xdg.CacheHome),
		RuntimeDir:     dirOr(s.RuntimeDir, xdg.RuntimeDir),
		KeyringService: s.KeyringService,
	}
	storage.ExportDir = filepath.Join(storage.DataDir, "exports")
	if s.ExportDir != "" {
		storage.ExportDir = dirOr(s.ExportDir, "")
	}
	for key, dir := range map[string]string{"data_dir": s.DataDir, "state_dir": s.StateDir, "cache_dir": s.CacheDir, "runtime_dir": s.RuntimeDir, "export_dir": s.ExportDir} {
		if dir != "" && dir != "~" && !strings.HasPrefix(dir, "~/") && !filepath.IsAbs(dir) {
			return domain.Storage{}, fmt.Errorf("storage.%s %q: give a full path (or one starting with ~/)", key, dir)
		}
	}
	if storage.KeyringService == "" {
		storage.KeyringService = defaultKeyring
	}
	return storage, nil
}

// chosenProfile is the profile in use: the one named, or the first when profiles
// exist and none is named (Config.Profile chooses it), so naming it or not is one
// instance.
func chosenProfile(cfg config.Config, profile string) string {
	if profile = strings.TrimSpace(profile); profile != "" || len(cfg.Profiles) == 0 {
		return profile
	}
	return cfg.Profiles[0].Name
}

// RememberInstance writes the instance StorageFor derived into a config that names
// none, so the files stay found if the config file moves. Only clients call it: the
// daemon cannot write the config. A config that cannot be written loses nothing
// while it stays where it is (the instance is derived from its path).
func RememberInstance(cfg config.Config, configPath string) error {
	if cfg.Storage.Instance != "" || configPath == "" {
		return nil
	}
	storage, err := StorageDirs(cfg)
	if err != nil {
		return err
	}
	base, err := baseInstance(cfg, configPath, storage)
	if err != nil {
		return err
	}
	if _, err := config.SetInstance(configPath, base); err != nil {
		return fmt.Errorf("record storage.instance in %s: %w", configPath, err)
	}
	return nil
}

// baseInstance is the config's instance: the one it names; else, for an install from
// before instances, the name its files were kept under; else one derived from the
// config file's path (pathInstance).
func baseInstance(cfg config.Config, configPath string, storage domain.Storage) (string, error) {
	if id := cfg.Storage.Instance; id != "" {
		if !validInstance.MatchString(id) {
			return "", fmt.Errorf("storage.instance %q: use letters, digits, '.', '_' and '-' (it names files)", id)
		}
		return id, nil
	}
	if len(cfg.Profiles) == 0 && cfg.User != "" {
		if legacy := domain.AccountKey(cfg.User); hasFiles(storage, legacy) {
			return legacy, nil
		}
	}
	return pathInstance(configPath), nil
}

// hasFiles reports whether files named for instance exist in storage's directories.
func hasFiles(storage domain.Storage, instance string) bool {
	probe := storage
	probe.Instance = instance
	for _, path := range []string{probe.CachePath(), probe.CryptoPath(), probe.SessionFile()} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// pathInstance is the instance of a config that names none: 64 bits of the hash of
// the config file's resolved path, so every program reading the same file (the
// daemon started by systemd, a client given --config) derives the same one, and two
// config files sharing directories derive different ones.
func pathInstance(configPath string) string {
	resolved := configPath
	// No file at all is one fixed instance, never the working directory's.
	if configPath != "" {
		if abs, err := filepath.Abs(configPath); err == nil {
			resolved = abs
		}
		if real, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = real
		}
	}
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:8])
}

// dirOr is a configured directory with "~" expanded, else the XDG one, each with
// kith's own subdirectory.
func dirOr(configured, xdgDir string) string {
	if configured == "" {
		if xdgDir == "" {
			return ""
		}
		return filepath.Join(xdgDir, "kith")
	}
	if configured == "~" || strings.HasPrefix(configured, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(configured, "~"))
		}
	}
	return filepath.Clean(configured)
}

// KeepRules is [storage] messages_per_room and its [[storage.rule]]s, checked: a count
// is negative (every message) or at least one, and a rule's match is a place entry.
func KeepRules(st config.Storage) (int, []domain.KeepRule, error) {
	base := st.MessagesKept()
	if base == 0 {
		return 0, nil, errors.New("config: [storage] messages_per_room: 0 would keep nothing; -1 keeps every message")
	}
	rules := make([]domain.KeepRule, 0, len(st.Rules))
	for i, r := range st.Rules {
		if _, ok := domain.ParseEntry(r.Match); !ok {
			return 0, nil, fmt.Errorf("config: [[storage.rule]] %d: match %q is no place — a room ID, room:<name>, space:<name>, tag:<name>, protocol:<network>, dm or group", i+1, r.Match)
		}
		if r.Messages == nil || *r.Messages == 0 {
			return 0, nil, fmt.Errorf("config: [[storage.rule]] %d (%s): messages is unset or 0; -1 keeps every message", i+1, r.Match)
		}
		rules = append(rules, domain.KeepRule{Match: r.Match, Messages: *r.Messages})
	}
	return base, rules, nil
}

// keepCheck is KeepRules for Validate.
func keepCheck(cfg config.Config) error {
	_, _, err := KeepRules(cfg.Storage)
	return err
}
