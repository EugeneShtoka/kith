//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// socketMode is owner-only: file permissions are the socket's whole authorization.
const socketMode = 0o600

func restrictSocket(path string) error {
	return os.Chmod(path, socketMode) //nolint:wrapcheck // Listen wraps it with what was being done
}

// secureDir checks dir is a real directory this user owns and makes it owner-only:
// whoever controls the directory can replace the socket.
func secureDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("daemon: socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("daemon: socket directory %s is not a directory (a symlink is refused)", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("daemon: socket directory %s belongs to uid %d, not to you; refusing to use it", dir, st.Uid)
	}
	if info.Mode().Perm() != socketDirMode {
		if err := os.Chmod(dir, socketDirMode); err != nil {
			return fmt.Errorf("daemon: make socket directory %s owner-only: %w", dir, err)
		}
	}
	return nil
}
