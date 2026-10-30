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
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
)

const meshUsage = "usage: kith mesh [--config path]"

// runMesh is `kith mesh`: this bridge's mesh as it sees it: the other bridges, the
// accounts each runs, when each was last heard, how far behind it is, its clock's gap,
// and the mesh store's size.
func runMesh(args []string) error {
	fs := flag.NewFlagSet("mesh", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse mesh flags: %w", err)
	}
	if fs.NArg() != 0 {
		return errors.New(meshUsage)
	}
	return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
		st, err := b.MeshStatus(ctx)
		if err != nil {
			return fmt.Errorf("mesh: %w", err)
		}
		showMesh(os.Stdout, st, time.Now())
		return nil
	})
}

// showMesh writes st as kith mesh prints it, as of now.
func showMesh(out io.Writer, st domain.MeshStatus, now time.Time) {
	var b strings.Builder
	fmt.Fprintf(&b, "This bridge: %s (mesh store %s)\n", st.Self, sizeText(st.StoreBytes))
	if st.Removed {
		b.WriteString("  Removed from its mesh: it no longer listens, and syncs only to pass that on. `kith join --bridge` joins a mesh again.\n")
	}
	writeAddresses(&b, st.Addresses)
	writeAccounts(&b, st.Accounts)
	if len(st.Peers) == 0 {
		b.WriteString("\nNo other bridge. On a machine with a kithd of its own, `kith join --bridge` with a line `kith pair` shows here joins it.\n")
	}
	for i := range st.Peers {
		p := &st.Peers[i]
		fmt.Fprintf(&b, "\n%s (%s)\n", deviceLabel(p.Bridge), p.Bridge.ID)
		writeAddresses(&b, p.Addresses)
		switch {
		case p.Forked:
			b.WriteString("  signed two different changes under one number: none of its changes are taken any more; remove it and pair it again\n")
		case p.Heard.IsZero():
			b.WriteString("  never heard\n")
		default:
			state := "not connected"
			if p.Connected {
				state = "connected"
			}
			fmt.Fprintf(&b, "  %s; heard %s ago; %d changes behind; %s\n",
				state, human.Duration(now.Sub(p.Heard).Truncate(time.Second)), p.Lag, gapText(p.ClockGap))
		}
		writeAccounts(&b, p.Accounts)
	}
	_, _ = io.WriteString(out, b.String())
}

// writeAddresses says where a bridge is dialed.
func writeAddresses(b *strings.Builder, addrs []string) {
	if len(addrs) == 0 {
		b.WriteString("  dials, is not dialed (no [remote] listen)\n")
		return
	}
	fmt.Fprintf(b, "  dialed at %s\n", strings.Join(addrs, ", "))
}

// writeAccounts lists the accounts a bridge runs.
func writeAccounts(b *strings.Builder, accounts []domain.MeshAccount) {
	if len(accounts) == 0 {
		b.WriteString("  runs no account\n")
		return
	}
	for _, a := range accounts {
		fmt.Fprintf(b, "  runs %s %s\n", a.Network, cmpLabel(a.Label, a.ID))
	}
}

// gapText says how far a peer's clock is off this bridge's.
func gapText(gap time.Duration) string {
	switch {
	case gap > time.Second:
		return "its clock " + human.Duration(gap.Truncate(time.Second)) + " ahead"
	case gap < -time.Second:
		return "its clock " + human.Duration((-gap).Truncate(time.Second)) + " behind"
	}
	return "clocks agree"
}

// sizeText is a size in bytes, as KiB or MiB.
func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
