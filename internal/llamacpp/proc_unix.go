//go:build !windows

package llamacpp

import (
	"fmt"
	"os/exec"
	"syscall"
)

// tree needs no state on unix: the child leads its own process group (pgid == pid).
type tree struct{}

// groupAttr puts the child in a process group of its own.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func adopt(*exec.Cmd) tree { return tree{} }
func (tree) release()      {}

// terminate asks the whole group to stop. An error means there was nobody to ask.
func (s *server) terminate() error {
	if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("llamacpp: signal process group: %w", err)
	}
	return nil
}

// kill ends a group that ignored terminate.
func (s *server) kill() { _ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) }
