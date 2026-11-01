package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"slices"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
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

// RememberBridge writes inv's bridge into the device file at path: over the entry with
// its ID (paired again, keeping the name given it), else as a new one, named by its
// first address's host. It returns the entry as written. `kith join` and the desktop's
// join both write it so.
func RememberBridge(path string, inv pairing.Invite) (config.Bridge, error) {
	was, err := config.LoadDevice(path)
	if err != nil {
		return config.Bridge{}, fmt.Errorf("the device file: %w", err)
	}
	now := was.Clone()
	host, _, _ := net.SplitHostPort(inv.Addresses[0]) // ParseURI checked it
	entry := config.Bridge{Name: host, Addresses: inv.Addresses, ID: inv.ID.String()}
	i := slices.IndexFunc(now.Bridges, func(b config.Bridge) bool {
		id, perr := devicekey.ParseID(b.ID)
		return perr == nil && id == inv.ID
	})
	if i >= 0 {
		entry.Name = now.Bridges[i].Name
		now.Bridges[i] = entry
	} else {
		now.Bridges = append(now.Bridges, entry)
	}
	if err := ValidateDevice(now); err != nil {
		return config.Bridge{}, fmt.Errorf("the device file: %w", err)
	}
	if _, err := config.UpdateDevice(path, was, now); err != nil {
		return config.Bridge{}, fmt.Errorf("write the device file: %w", err)
	}
	return entry, nil
}
