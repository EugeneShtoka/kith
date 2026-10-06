package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
)

// The daemon is the config file's one writer: a client changes it with UpdateConfig,
// on the revision it read, and every client hears the result. The revision is the
// file's contents, so a hand edit made meanwhile refuses a change made on the file
// before it, rather than being written over.

// ConfigFile is the config file the daemon serves: its path and the [[profile]] it
// runs as. Build it with NewConfigFile.
type ConfigFile struct {
	path, profile string
	// mu serializes writes, each read-check-write-apply as one.
	mu sync.Mutex
}

// NewConfigFile is the config at path, served as profile.
func NewConfigFile(path, profile string) *ConfigFile {
	return &ConfigFile{path: path, profile: profile}
}

// Read is the configuration as the daemon serves it (its profile applied), and the
// file's revision.
func (f *ConfigFile) Read() (config.Snapshot, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return config.Snapshot{}, fmt.Errorf("daemon: read the config: %w", err)
	}
	cfg, err := config.Decode(string(data))
	if err != nil {
		return config.Snapshot{}, err //nolint:wrapcheck // config's errors say what is wrong
	}
	served, err := cfg.Profile(f.profile)
	if err != nil {
		return config.Snapshot{}, err //nolint:wrapcheck // as above
	}
	return config.Snapshot{Config: served, Revision: config.Revision(data)}, nil
}

// Write makes cfg the config file, if the file is still at base and check passes it,
// then puts it in force with apply. cfg is as a client has it, its profile applied;
// what is written keeps the account in [[profile]] blocks where the file keeps it
// there. It returns what was written.
func (f *ConfigFile) Write(ctx context.Context, base string, cfg config.Config, check CheckConfig, apply Reload) (config.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.Snapshot{}, fmt.Errorf("daemon: read the config: %w", err)
	}
	if config.Revision(data) != base {
		return config.Snapshot{}, api.ErrConfigMoved
	}
	written := cfg.Clone()
	if data != nil {
		if onDisk, derr := config.Decode(string(data)); derr == nil && len(onDisk.Profiles) > 0 {
			written.Homeserver, written.User = onDisk.Homeserver, onDisk.User // the account stays per profile
		}
	}
	served, err := written.Profile(f.profile)
	if err != nil {
		return config.Snapshot{}, err //nolint:wrapcheck // config's errors say what is wrong
	}
	if check != nil {
		if err := check(ctx, served); err != nil {
			return config.Snapshot{}, err
		}
	}
	if err := config.Save(f.path, written); err != nil {
		return config.Snapshot{}, err //nolint:wrapcheck // config's errors name the file
	}
	if apply != nil {
		if err := apply(ctx); err != nil {
			return config.Snapshot{}, err
		}
	}
	return f.Read()
}
