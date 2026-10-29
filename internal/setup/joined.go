package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// JoinedBridge is the bridge a client of the config at configPath dials: the first
// [[bridge]] its device file names (`kith join` writes it). False when it names none and
// kithd is this machine's. It writes nothing.
func JoinedBridge(configPath string) (config.Bridge, bool, error) {
	device := config.DevicePath(configPath)
	if _, err := os.Stat(device); errors.Is(err, fs.ErrNotExist) {
		return config.Bridge{}, false, nil
	}
	dev, err := config.LoadDevice(device)
	if err != nil {
		return config.Bridge{}, false, fmt.Errorf("reading %s: %w", device, err)
	}
	if len(dev.Bridges) == 0 {
		return config.Bridge{}, false, nil
	}
	return dev.Bridges[0], true, nil
}

// ClientStorage is where a client of a bridge elsewhere keeps its own files (its device
// key, logs, the media cache, exports) for the config at configPath, and whether its key
// may be a file: the config's storage when the file is here, else the defaults. Every
// client program finds its key by it, so they agree; it writes no config.
func ClientStorage(configPath string) (domain.Storage, bool, error) {
	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		storage, serr := StorageFor(config.Config{}, configPath)
		return storage, false, serr
	}
	if _, err := config.SplitDevice(configPath, config.DevicePath(configPath)); err != nil {
		return domain.Storage{}, false, fmt.Errorf("reading %s: %w", configPath, err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return domain.Storage{}, false, fmt.Errorf("reading %s: %w", configPath, err)
	}
	storage, err := StorageFor(cfg, configPath)
	return storage, cfg.AllowTokenFile, err
}
