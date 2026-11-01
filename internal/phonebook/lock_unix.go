//go:build unix

package phonebook

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir holds dir against kith's other writers of it, in this process or any other,
// until the returned func is called: an advisory lock on the folder itself, so nothing
// of kith's is kept in it. A folder that cannot be locked (a file system without locks)
// is written unlocked, as every other program writes it.
func lockDir(dir string) func() {
	f, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return func() {}
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return func() {}
	}
	// Closing the folder lets the lock go.
	return func() { _ = f.Close() }
}
