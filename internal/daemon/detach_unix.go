//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"os/exec"
)

// detach starts kithd with setsid so it survives this process and its terminal.
// context.Background() on purpose: exec.CommandContext would kill the daemon when
// the client's context ends.
func detach(daemonArgs []string) error {
	path, err := exec.LookPath(daemonBinary)
	if err != nil {
		return fmt.Errorf("daemon: %s is not on PATH: %w", daemonBinary, err)
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		return fmt.Errorf("daemon: cannot detach %s without setsid: %w", daemonBinary, err)
	}
	args := append([]string{path}, daemonArgs...)
	cmd := exec.CommandContext(context.Background(), "setsid", args...) // #nosec G204 -- path is LookPath("kithd") and the profile is a config-file name, no shell involved
	// Neither stdout nor stderr is inherited: the daemon must not write over a running
	// TUI. It logs to the journal, or else DaemonLogPath (the --log-file it is given).
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: start %s: %w", daemonBinary, err)
	}
	// Reap setsid; it exits as soon as the daemon is running, and the daemon's own
	// failures are in its log.
	go func() { _ = cmd.Wait() }()
	return nil
}

// autostartHint is shown after the daemon had to be started by hand.
func autostartHint() string {
	return "To have it start at login: systemctl --user enable --now " + unitName
}
