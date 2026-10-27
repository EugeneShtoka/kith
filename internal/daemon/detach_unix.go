//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"os/exec"
)

// detach starts binary (kithd, kith-notify) under setsid so it survives this process
// and its terminal. context.Background() on purpose: CommandContext would kill it with
// the client.
func detach(binary string, binaryArgs []string) error {
	path, err := Binary(binary)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		return fmt.Errorf("daemon: cannot detach %s without setsid: %w", binary, err)
	}
	args := append([]string{path}, binaryArgs...)
	cmd := exec.CommandContext(context.Background(), "setsid", args...) // #nosec G204 -- path is Binary's kithd or kith-notify and the arguments kith's own flags, no shell involved
	// Nothing inherited: it must not write over a running TUI. It logs to the journal
	// or its --log-file.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: start %s: %w", binary, err)
	}
	// Reap setsid, which exits once the process runs; its failures are in its log.
	go func() { _ = cmd.Wait() }()
	return nil
}

// autostartHint is shown after the daemon had to be started on demand.
func autostartHint(unit string) string {
	return "To have it start at login: systemctl --user enable --now " + unit
}
