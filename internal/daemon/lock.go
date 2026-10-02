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
// beside the socket, taken before any store is opened. Two processes sharing one
// device's olm/megolm state corrupt it irreparably.
type Lock struct {
	fl     *flock.Flock
	socket string
}

// Acquire takes the exclusive lock for the instance's daemon.
//
// held is false, with no error, when another daemon already holds it: two clients
// racing to auto-spawn is expected, and the caller exits silently.
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

// Listen opens the daemon's listening socket, removing a stale one first. Safe only
// because we hold the lock; the package-level Listen never unlinks.
func (l *Lock) Listen(ctx context.Context) (net.Listener, error) {
	if err := os.Remove(l.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("daemon: remove stale socket %s: %w", l.socket, err)
	}
	return Listen(ctx, l.socket)
}

// Release removes the socket (while still holding the lock) and drops the lock.
// The lock file itself stays: unlinking it is the classic flock race that lets two
// daemons each hold "the" lock.
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
