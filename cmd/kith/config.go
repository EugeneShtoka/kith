package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const configUsage = "usage: kith config [--config path] conflicts | keep <key> <n>"

// runConfig is `kith config`: the keys of the config that bridges of the mesh wrote
// concurrently with different values (conflicts), and keeping one of a key's values
// (keep), which ends its conflict on every bridge.
func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse config flags: %w", err)
	}
	switch {
	case fs.NArg() == 1 && fs.Arg(0) == "conflicts":
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
			conflicts, err := b.ConfigConflicts(ctx)
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			showConflicts(os.Stdout, conflicts)
			return nil
		})
	case fs.NArg() == 3 && fs.Arg(0) == "keep":
		n, err := strconv.Atoi(fs.Arg(2))
		if err != nil || n < 1 {
			return errors.New(configUsage)
		}
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
			if err := b.KeepConfigValue(ctx, fs.Arg(1), n-1); err != nil {
				return fmt.Errorf("config: %w", err)
			}
			fmt.Printf("Kept value %d of %s on every bridge.\n", n, fs.Arg(1))
			return nil
		})
	}
	return errors.New(configUsage)
}

// showConflicts writes each key in conflict with its values, numbered as keep takes
// them, the one in force first.
func showConflicts(out io.Writer, conflicts []domain.ConfigConflict) {
	if len(conflicts) == 0 {
		_, _ = fmt.Fprintln(out, "No conflicts: every bridge of the mesh holds one value of each key.")
		return
	}
	var b strings.Builder
	for _, c := range conflicts {
		fmt.Fprintf(&b, "%s\n", c.Path)
		for i := range c.Values {
			v := &c.Values[i]
			inForce := ""
			if i == 0 {
				inForce = " (in force)"
			}
			fmt.Fprintf(&b, "  %d. %s, by %s at %s%s\n", i+1, indentValue(v.Value), deviceLabel(v.Bridge), v.At.Local().Format("2006-01-02 15:04"), inForce)
		}
	}
	b.WriteString("`kith config keep <key> <n>` keeps one, on every bridge.\n")
	_, _ = io.WriteString(out, b.String())
}

// indentValue is a value shown on a numbered line: a list of tables, written over
// several lines, continues indented under it.
func indentValue(v string) string {
	return strings.ReplaceAll(v, "\n", "\n     ")
}
