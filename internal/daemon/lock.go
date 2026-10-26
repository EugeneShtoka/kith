package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/gofrs/flock"
)

// Lock is the daemon's single-instance guard: an exclusive advisory lock on a file
// beside the socket, taken before any store opens. Two processes sharing a device's
// olm/megolm state corrupt it irreparably.
type Lock struct {
	fl     *flock.Flock
	socket string
}

// Acquire takes the exclusive lock for the instance's daemon. held is false, with no
// error, when another daemon holds it (racing auto-spawns are expected).
func Acquire(storage domain.Storage) (lock *Lock, held bool, err error) {
	socket, err := SocketPath(storage)
	if err != nil {
		return nil, false, err
	}
	if mkerr := makeSocketDir(filepath.Dir(socket)); mkerr != nil {
		return nil, false, mkerr
	}
	path := storage.LockPath()
	fl := flock.New(path)
	held, err = fl.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	if !held {
		return nil, false, nil
	}
	return &Lock{fl: fl, socket: socket}, true, nil
}

// Socket returns the path of the socket this lock governs.
func (l *Lock) Socket() string { return l.socket }

// Listen opens the listening socket, removing a stale one first; safe only under the
// lock, which is why the package-level Listen never unlinks.
func (l *Lock) Listen(ctx context.Context) (net.Listener, error) {
	if err := os.Remove(l.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("daemon: remove stale socket %s: %w", l.socket, err)
	}
	return Listen(ctx, l.socket)
}

// Release removes the socket while still locked, then unlocks. The lock file stays:
// unlinking it is the classic flock race letting two daemons each hold "the" lock.
func (l *Lock) Release() error {
	rmErr := os.Remove(l.socket)
	if errors.Is(rmErr, os.ErrNotExist) {
		rmErr = nil
	}
	unlockErr := l.fl.Unlock()
	if rmErr != nil {
		return fmt.Errorf("daemon: remove socket: %w", rmErr)
	}
	if unlockErr != nil {
		return fmt.Errorf("daemon: unlock: %w", unlockErr)
	}
	return nil
}

// AcquireNotifier takes the instance's kith-notify lock, so however often kith starts
// it, one shows each notification. held is false, with no error, when another holds it;
// release gives it up.
func AcquireNotifier(storage domain.Storage) (release func() error, held bool, err error) {
	socket, err := SocketPath(storage)
	if err != nil {
		return nil, false, err
	}
	if mkerr := makeSocketDir(filepath.Dir(socket)); mkerr != nil {
		return nil, false, mkerr
	}
	path := storage.NotifierLockPath()
	fl := flock.New(path)
	if held, err = fl.TryLock(); err != nil {
		return nil, false, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	if !held {
		return nil, false, nil
	}
	return fl.Unlock, true, nil
}
