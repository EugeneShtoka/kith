package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/human"
)

// runStatus is `kith status`: kithd's version, and each account as held or stuck and
// why (design-failover.md, step 1): what its heartbeat says.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse status flags: %w", err)
	}
	if fs.NArg() != 0 {
		return errors.New("usage: kith status [--config path]")
	}
	return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
		version, err := b.Version(ctx)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		rows, err := b.Networks(ctx)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		showStatus(os.Stdout, version, rows, time.Now())
		return nil
	})
}

// showStatus writes version and rows as kith status prints them, as of now.
func showStatus(out io.Writer, version string, rows []daemon.NetworkStatus, now time.Time) {
	var b strings.Builder
	fmt.Fprintf(&b, "kithd %s\n", version)
	if len(rows) == 0 {
		b.WriteString("No accounts. `kith login <network>` adds one.\n")
	}
	for _, n := range rows {
		held, why := n.Held(now)
		state := "held"
		if !held {
			state = "stuck"
		}
		heard := "never heard"
		if !n.AliveAt.IsZero() {
			heard = "heard " + human.Duration(now.Sub(n.AliveAt).Truncate(time.Second)) + " ago"
		}
		fmt.Fprintf(&b, "%s %s: %s (%s)", n.Network, n.Account, state, heard)
		if why != "" {
			b.WriteString(": " + why)
		}
		b.WriteString("\n")
	}
	_, _ = io.WriteString(out, b.String())
}
