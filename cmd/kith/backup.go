package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// runBackup is `kith backup …` and runRestore `kith restore …`. kithd does both, since
// it owns the cache (kith links no SQLite), so these run it in this terminal with the
// same arguments; it asks for the backup key and reports.
func runBackup(args []string) error  { return runInKithd("backup", args) }
func runRestore(args []string) error { return runInKithd("restore", args) }

// versionTimeout bounds `kithd --version`, which only prints.
const versionTimeout = 10 * time.Second

// runInKithd runs `kithd <command> <args…>` here, exiting with its status. The kithd is
// this kith's own build (daemon.Binary, then a version check): another build may not
// know the command, or read the files differently.
func runInKithd(command string, args []string) error {
	kithd, err := daemon.Binary("kithd")
	if err != nil {
		return fmt.Errorf("%s is kithd's to do: %w", command, err)
	}
	if err = sameBuild(kithd); err != nil {
		return fmt.Errorf("%s is kithd's to do, but %w", command, err)
	}
	cmd := exec.CommandContext(context.Background(), kithd, append([]string{command}, args...)...) // #nosec G204 G702 -- kithd is this kith's own build, given this user's own arguments; no shell involved
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// An interrupt reaches kithd too; wait for it to clean up and say so.
	signal.Ignore(os.Interrupt)
	err = cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		// kithd usually says why itself; this line makes sure a failure is never silent.
		fmt.Fprintf(os.Stderr, "kith: kithd %s failed (%v)\n", command, exit)
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("run kithd %s: %w", command, err)
	}
	return nil
}

// sameBuild checks that kithd reports the build this kith is.
func sameBuild(kithd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, kithd, "--version").Output() // #nosec G204 -- kithd from daemon.Binary, a fixed flag
	if err != nil {
		return fmt.Errorf("%s --version failed: %w", kithd, err)
	}
	theirs, ours := oneLine(string(out)), oneLine(buildinfo.String("kithd"))
	if theirs != ours {
		return fmt.Errorf("%s is another build than this kith:\n  kith:  %s\n  kithd: %s\ninstall kith and kithd together, or put the matching kithd first on PATH",
			kithd, oneLine(buildinfo.String("kith")), theirs)
	}
	return nil
}

// oneLine joins a multi-line version report into one line.
func oneLine(report string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(report), "\n", ",")), " ")
}
