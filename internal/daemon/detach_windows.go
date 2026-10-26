//go:build windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Process-creation flags from winbase.h (not in package syscall).
const (
	detachedProcess = 0x00000008
	// createBreakawayFromJob leaves a kill-on-close job (SSH, IDE terminals) so the
	// daemon does not die with the terminal.
	createBreakawayFromJob = 0x01000000
)

// detach starts binary.exe (kithd, kith-notify) with no console in its own process
// group, the Windows `setsid kithd`. context.Background() as in detach_unix.go.
func detach(binary string, binaryArgs []string) error {
	path, err := binaryPath(binary)
	if err != nil {
		return err
	}
	args := binaryArgs
	start := func(flags uint32) error {
		cmd := exec.CommandContext(context.Background(), path, args...) // #nosec G204 -- path is kithd.exe or kith-notify.exe beside us or on PATH; the arguments are kith's own flags
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		// Not the client's console: the daemon logs to its --log-file.
		cmd.Stdout, cmd.Stderr = nil, nil
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", path, err)
		}
		// Reap it; the daemon's own failures are in its log file.
		go func() { _ = cmd.Wait() }()
		return nil
	}
	base := uint32(detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP)
	// Breakaway first; a job forbidding it fails CreateProcess with ACCESS_DENIED, and
	// the plain start is then the best there is.
	if err := start(base | createBreakawayFromJob); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := start(base); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	return nil
}

// binaryPath finds binary.exe beside this executable (an unzipped release is often not
// on PATH), then on PATH.
func binaryPath(binary string) (string, error) {
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), binary+".exe")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, nil
		}
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("daemon: %s.exe is neither beside kith.exe nor on PATH: %w", binary, err)
	}
	return path, nil
}

func autostartHint(string) string {
	return "(Windows has no login autostart for kithd yet; kith starts it on demand.)"
}
