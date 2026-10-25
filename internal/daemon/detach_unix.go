//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"os/exec"
)

// detach starts kithd under setsid so it survives this process and its terminal.
// context.Background() on purpose: CommandContext would kill the daemon with the client.
func detach(daemonArgs []string) error {
	path, err := exec.LookPath(daemonBinary)
	if err != nil {
		return fmt.Errorf("daemon: %s is not on PATH: %w", daemonBinary, err)
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		return fmt.Errorf("daemon: cannot detach %s without setsid: %w", daemonBinary, err)
	}
	args := append([]string{path}, daemonArgs...)
	cmd := exec.CommandContext(context.Background(), "setsid", args...) // #nosec G204 -- path is LookPath("kithd") and the arguments kith's own flags, no shell involved
	// Nothing inherited: the daemon must not write over a running TUI. It logs to the
	// journal or its --log-file.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: start %s: %w", daemonBinary, err)
	}
	// Reap setsid, which exits once the daemon runs; daemon failures are in its log.
	go func() { _ = cmd.Wait() }()
	return nil
}

// autostartHint is shown after the daemon had to be started on demand.
func autostartHint(unit string) string {
	return "To have it start at login: systemctl --user enable --now " + unit
}
