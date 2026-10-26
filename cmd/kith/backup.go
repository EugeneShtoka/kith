package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

// runBackup is `kith backup …` and runRestore `kith restore …`. kithd does both, since
// it owns the cache (kith links no SQLite), so these run it in this terminal with the
// same arguments; it asks for the passphrase and reports.
func runBackup(args []string) error  { return runInKithd("backup", args) }
func runRestore(args []string) error { return runInKithd("restore", args) }

// runInKithd runs `kithd <command> <args…>` here, exiting with its status.
func runInKithd(command string, args []string) error {
	kithd, err := exec.LookPath("kithd")
	if err != nil {
		return fmt.Errorf("%s is kithd's to do, and kithd is not on PATH: %w", command, err)
	}
	cmd := exec.CommandContext(context.Background(), kithd, append([]string{command}, args...)...) // #nosec G204 G702 -- kithd from PATH, given this user's own arguments; no shell involved
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// An interrupt reaches kithd too; wait for it to clean up and say so.
	signal.Ignore(os.Interrupt)
	err = cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		os.Exit(exit.ExitCode()) // kithd has said why
	}
	if err != nil {
		return fmt.Errorf("run kithd %s: %w", command, err)
	}
	return nil
}
