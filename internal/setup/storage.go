package setup

import (
	"crypto/rand"
	"encoding/hex"
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
// chosen (cfg is that profile's, as Config.Profile returns it). An instance not yet
// chosen is chosen now and written into the config: the name an install from before
// kept its files under, when they exist, else a new one.
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
	for key, dir := range map[string]string{"data_dir": s.DataDir, "state_dir": s.StateDir, "cache_dir": s.CacheDir, "runtime_dir": s.RuntimeDir} {
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

// baseInstance is the config's instance: the one it names; else, for an install from
// before instances, the name its files were kept under; else a new one. Either of the
// last two is written into the config, so it stays.
func baseInstance(cfg config.Config, configPath string, storage domain.Storage) (string, error) {
	if id := cfg.Storage.Instance; id != "" {
		if !validInstance.MatchString(id) {
			return "", fmt.Errorf("storage.instance %q: use letters, digits, '.', '_' and '-' (it names files)", id)
		}
		return id, nil
	}
	chosen := ""
	if len(cfg.Profiles) == 0 && cfg.User != "" {
		if legacy := domain.AccountKey(cfg.User); hasFiles(storage, legacy) {
			chosen = legacy
		}
	}
	if chosen == "" {
		chosen = newInstance()
	}
	if configPath == "" {
		return chosen, nil
	}
	kept, err := config.SetInstance(configPath, chosen)
	if err != nil {
		return "", fmt.Errorf("record storage.instance in %s (kith needs to remember it; set it by hand if the file is read-only): %w", configPath, err)
	}
	return kept, nil
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

// newInstance is a fresh instance ID: 64 random bits, as hex.
func newInstance() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	return hex.EncodeToString(b[:])
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
