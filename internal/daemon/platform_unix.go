//go:build !windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand runs a user-configured command line through the platform shell.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command) // #nosec G204 -- command is user config; the payload goes on stdin, never interpolated
}

// graphicalSession reports why there is no clipboard to write to, or nil.
func graphicalSession() error {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		return errors.New("no graphical session in the daemon's environment " +
			"(try `systemctl --user import-environment WAYLAND_DISPLAY`)")
	}
	return nil
}

// socketMode is owner-only: the socket reaches everything the account can do, and
// file permissions are its whole authorization.
const socketMode = 0o600

func restrictSocket(path string) error {
	return os.Chmod(path, socketMode) //nolint:wrapcheck // Listen wraps it with what was being done
}

// secureDir checks that dir is a real directory this user owns, and makes it
// owner-only: whoever controls the directory can replace the socket in it.
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
