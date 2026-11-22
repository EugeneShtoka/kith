package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
)

const meshUsage = "usage: kith mesh [--config path] [recovery-key | recover]"

// runMesh is `kith mesh`: this bridge's mesh as it sees it: the other bridges, the
// accounts each runs, when each was last heard, how far behind it is, its clock's gap,
// the mesh store's size, and each mailbox. `kith mesh recovery-key` makes the mesh's
// recovery key; `kith mesh recover` starts the mesh anew from a mailbox with it.
func runMesh(args []string) error {
	fs := flag.NewFlagSet("mesh", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse mesh flags: %w", err)
	}
	switch {
	case fs.NArg() == 1 && fs.Arg(0) == "recovery-key":
		return withDevices(*configPath, func(ctx context.Context, b *daemon.Remote) error {
			return makeMeshRecoveryKey(ctx, b, os.Stdout)
		})
	case fs.NArg() == 1 && fs.Arg(0) == "recover":
		ask := terminalAsk()
		if ask == nil {
			return errors.New("mesh recover: type the recovery key at a terminal")
		}
		typed, err := ask("The mesh's recovery key")
		if err != nil {
			return fmt.Errorf("mesh recover: %w", err)
		}
		// No deadline: the mailbox may hold weeks of changes to bring back.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		at, err := reachHere(ctx, *configPath)
		if err != nil || at.backend == nil {
			return err
		}
		defer at.backend.Stop()
		return recoverMesh(ctx, at.backend, typed, os.Stdout)
	case fs.NArg() != 0:
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
	for i := range st.Mailboxes {
		writeMailbox(&b, &st.Mailboxes[i], now)
	}
	_, _ = io.WriteString(out, b.String())
}

// makeMeshRecoveryKey has b make a recovery key for its mesh, and shows it.
func makeMeshRecoveryKey(ctx context.Context, b *daemon.Remote, out io.Writer) error {
	key, err := b.MakeMeshRecoveryKey(ctx)
	if err != nil {
		return fmt.Errorf("mesh recovery-key: %w", err)
	}
	_, err = fmt.Fprintf(out, "The mesh's recovery key:\n\n  %s\n\n"+
		"Keep it on paper or in a password manager: kith keeps no copy, and this is the one time it is shown. "+
		"If every bridge is lost, a new kithd paired with the mailbox (`kith mailbox add`) brings the mesh back with "+
		"`kith mesh recover`. Making another replaces it; this one still opens what was sealed until then.\n", key)
	if err != nil {
		return fmt.Errorf("mesh recovery-key: %w", err)
	}
	return nil
}

// recoverMesh has b start its mesh anew from its mailbox with key, and says what came
// back.
func recoverMesh(ctx context.Context, b *daemon.Remote, key string, out io.Writer) error {
	got, mailbox, err := b.RecoverMesh(ctx, key)
	if err != nil {
		return fmt.Errorf("mesh recover: %w", err)
	}
	var s strings.Builder
	fmt.Fprintf(&s, "Recovered %d keys (read positions, drafts, config, verdicts…) from the mailbox %s. This bridge now starts the mesh.\n", got.Keys, mailbox)
	if len(got.Members) > 0 {
		s.WriteString("The old mesh's members are listed as removed here; pair each again (`kith pair`, `kith join --bridge`):\n")
	}
	for _, d := range got.Members {
		fmt.Fprintf(&s, "  %s (%s), %s\n", deviceLabel(d), d.ID, d.Role)
	}
	if _, err := io.WriteString(out, s.String()); err != nil {
		return fmt.Errorf("mesh recover: %w", err)
	}
	return nil
}

// writeMailbox says how a mailbox stands: where it is dialed, when it was heard, its
// store's size, and how many changes of each origin it lacks of those this bridge holds.
func writeMailbox(b *strings.Builder, mb *domain.MeshMailbox, now time.Time) {
	fmt.Fprintf(b, "\nMailbox %s (%s)\n", deviceLabel(mb.Mailbox), mb.Mailbox.ID)
	fmt.Fprintf(b, "  dialed at %s\n", strings.Join(mb.Addresses, ", "))
	if mb.Heard.IsZero() {
		b.WriteString("  never heard\n")
		return
	}
	state := "not connected"
	if mb.Connected {
		state = "connected"
	}
	fmt.Fprintf(b, "  %s; heard %s ago; holds %s\n", state, human.Duration(now.Sub(mb.Heard).Truncate(time.Second)),
		sizeText(int64(min(mb.StoreBytes, 1<<62)))) // #nosec G115 -- clamped
	if len(mb.Lag) == 0 {
		b.WriteString("  holds every change this bridge does\n")
	}
	for _, l := range mb.Lag {
		fmt.Fprintf(b, "  lacks %d changes of %s\n", l.Ops, l.Origin)
	}
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
