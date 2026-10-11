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

// The daemon is the config file's one writer. Clients write via UpdateConfig on the
// revision they read; the revision is the file's contents, so a hand edit made
// meanwhile refuses the stale change instead of being overwritten.

// unwritable is a valid config the daemon could not write (e.g. read-only file
// system): its failure, not the client's input.
type unwritable struct{ err error }

func (u unwritable) Error() string { return "daemon: write the config: " + u.err.Error() }
func (u unwritable) Unwrap() error { return u.err }

// ConfigFile is the config file the daemon serves and the [[profile]] it runs as.
type ConfigFile struct {
	path, profile string
	// mu serializes writes, each read-check-write-apply as one.
	mu sync.Mutex
}

func NewConfigFile(path, profile string) *ConfigFile {
	return &ConfigFile{path: path, profile: profile}
}

// Read is the served configuration (profile applied) and the file's revision.
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

// Write makes cfg the config file if the file is still at base and check passes, then
// applies it, returning what was written. cfg has its profile applied; the account
// stays in [[profile]] blocks where the file keeps it there.
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
		return config.Snapshot{}, unwritable{err}
	}
	if apply != nil {
		if err := apply(ctx); err != nil {
			return config.Snapshot{}, err
		}
	}
	return f.Read()
}
