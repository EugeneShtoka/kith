//go:build !windows

package spell

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the engine as the leader of a process group of its own, so killing
// it reaches whatever it started (a wrapper script's hunspell).
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killEngine kills the engine's whole group; the engine itself if that fails.
func killEngine(cmd *exec.Cmd) {
	if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
		_ = cmd.Process.Kill()
	}
}
