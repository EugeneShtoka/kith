package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Binary is the path of binary (kithd, kith-notify) to run now: the one installed
// beside this executable, which packages and Nix ship as the same build, else the first
// on PATH. A path kept for later (a systemd unit) stays a PATH lookup instead, since
// this executable's own path may be a store path an upgrade replaces.
func Binary(binary string) (string, error) {
	name := binary
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
			self = resolved
		}
		sibling := filepath.Join(filepath.Dir(self), name)
		if info, err := os.Stat(sibling); err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
			return sibling, nil
		}
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("daemon: %s is neither beside %s nor on PATH: %w", name, filepath.Base(os.Args[0]), err)
	}
	return path, nil
}
