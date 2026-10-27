package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// ErrDaemonRunning is a restore refused because kithd is running on the files it
// would replace.
var ErrDaemonRunning = errors.New("kithd is running on this config; stop it first")

// ErrWouldReplace is a restore refused because files are already there, and it was
// not asked to set them aside.
var ErrWouldReplace = errors.New("files are already there")

// ErrWrongKey is a backup the key given does not open, or a key that is not one.
var ErrWrongKey = errors.New("the key does not open this backup")

// ErrDamaged is a backup that is cut short, altered, or not a kith backup.
var ErrDamaged = errors.New("the backup is damaged or not a kith backup")

// smallLimit bounds what a restore holds in memory per small file.
const smallLimit = 64 << 20

// asideInfix marks what a restore set aside, before the time it did.
const asideInfix = ".before-restore-"

// Login is an account of the restored config, to log in to again.
type Login struct {
	Network, Account string
}

// Restored is a restore done: where each file went, what was there before and where
// it was set aside, what was not restored and why, and the accounts to log in to.
// Pictures counts the pictures put back in the picture cache; PicturesThere those it
// held already, which stay as they were.
type Restored struct {
	Files          []string
	Aside          []string
	Left           []string
	Logins         []Login
	Pictures       int
	PicturesThere  int
	PicturesFolder string
}

// Restore puts the backup at archive back for the config at configPath: the config
// itself, then the cache, records and pictures where that config keeps them on this
// machine, and the backup key's public half, so this machine's backups go on to the
// same key. Files already there are refused unless replace, which sets them aside
// under a ".before-restore-<time>" name; pictures are added to the picture cache,
// which keeps those it holds. key, the private backup key, is asked once the refusals
// that need no backup are past. Until everything is read and checked nothing in place
// is touched, so a wrong key, a damaged backup or a full disk leaves it as it was.
// cacheKey is the key the restored storage's cache is encrypted under on this machine.
func Restore(ctx context.Context, archive, configPath string, replace bool, key func() (*Key, error),
	cacheKey func(domain.Storage) ([]byte, error),
) (Restored, error) {
	r := restore{configPath: configPath, replace: replace, cacheKey: cacheKey}
	defer r.cleanup()
	if err := r.lockExisting(); err != nil {
		return Restored{}, err
	}
	f, err := os.Open(archive) // #nosec G304 -- the backup this user named
	if err != nil {
		return Restored{}, fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = f.Close() }()
	id, err := key()
	if err != nil {
		return Restored{}, err
	}
	r.id = id
	if err := r.read(ctx, f); err != nil {
		return Restored{}, err
	}
	return r.swap(ctx)
}

// restore is one restore's state: the locks it holds, what it staged, and what it
// will report.
type restore struct {
	configPath string
	replace    bool
	// cacheKey finds this machine's key for a storage's cache; key is the one found
	// for the restored storage, and backupKey the one the backup's cache is under (nil:
	// a plain cache).
	cacheKey       func(domain.Storage) ([]byte, error)
	key, backupKey []byte
	// id is the backup key the backup opened with.
	id *Key

	locks    []*daemon.Lock
	lockedAt []string // each held lock's path

	cfg     config.Config
	storage domain.Storage
	// small holds every entry but the cache, by name, in archive order.
	small     map[string][]byte
	smallMode map[string]fs.FileMode
	order     []string
	cache     string // the adopted cache, ready to move into place
	// media is the picture cache's folder; pictures, the folder the backup's are staged
	// in beside it, and pictureNames their names there.
	media, pictures string
	pictureNames    []string
	staged          []string

	done Restored
}

// lockExisting refuses a restore over an existing config unless replace, and takes
// the daemon lock of the config there, so its kithd cannot start (or is found
// running) while files change under it.
func (r *restore) lockExisting() error {
	_, err := os.Stat(r.configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if !r.replace {
		return fmt.Errorf("restore: %w: %s (--replace sets them aside)", ErrWouldReplace, r.configPath)
	}
	storage, ok := storageOf(r.configPath)
	if !ok {
		// A config that does not read names no daemon to wait for; the restored one's
		// is locked once read.
		return nil
	}
	return r.lock(storage)
}

// storageOf is where the config at path keeps its files; false when it does not read.
func storageOf(path string) (domain.Storage, bool) {
	cfg, err := config.Load(path)
	if err != nil {
		return domain.Storage{}, false
	}
	storage, err := setup.StorageFor(cfg, path)
	return storage, err == nil
}

// lock takes storage's daemon lock unless held already; a lock another holds is a
// running kithd.
func (r *restore) lock(storage domain.Storage) error {
	if slices.Contains(r.lockedAt, storage.LockPath()) {
		return nil
	}
	l, held, err := daemon.Acquire(storage)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if !held {
		return fmt.Errorf("restore: %w (its instance is %s)", ErrDaemonRunning, storage.Instance)
	}
	r.locks = append(r.locks, l)
	r.lockedAt = append(r.lockedAt, storage.LockPath())
	return nil
}

// cleanup removes whatever was staged and not moved into place, and releases the
// locks.
func (r *restore) cleanup() {
	for _, p := range r.staged {
		_ = os.RemoveAll(p)
	}
	for _, l := range r.locks {
		_ = l.Release()
	}
}
