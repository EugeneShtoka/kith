package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const mailboxUsage = "usage: kith mailbox add [--name name] [--config path] <kith://pair?… line kith-mailbox pair shows>"

// runMailbox is `kith mailbox add`: this machine's kithd pairs with a mailbox
// (kith-mailbox), which the mesh's bridges leave their sealed changes with and pick
// each other's up from.
func runMailbox(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New(mailboxUsage)
	}
	fs := flag.NewFlagSet("mailbox add", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	name := fs.String("name", "mailbox", "how the mesh lists the mailbox")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("parse mailbox flags: %w", err)
	}
	if fs.NArg() != 1 {
		return errors.New(mailboxUsage)
	}
	// No deadline: the pairing ends itself when it expires at the mailbox.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachHere(ctx, *configPath)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	return addMailbox(ctx, at.backend, fs.Arg(0), *name, os.Stdout)
}

// addMailbox has b pair with the mailbox uri names and says how it went.
func addMailbox(ctx context.Context, b *daemon.Remote, uri, name string, out io.Writer) error {
	outcome, err := b.AddMailbox(ctx, uri, name, func(digits string) {
		_, _ = fmt.Fprintf(out, "This bridge shows %s. Confirm at the mailbox (kith-mailbox pair) that it shows the same.\n", digits)
	})
	if err != nil {
		return fmt.Errorf("mailbox: %w", err)
	}
	if outcome != domain.PairingConfirmed {
		return fmt.Errorf("mailbox: %w", daemon.PairingOutcomeErr(outcome))
	}
	_, _ = fmt.Fprintln(out, "Added. The mesh's bridges leave their changes with it, sealed, and pick each other's up there; "+
		"`kith mesh` shows how far behind it is. A bridge that never meets this one learns of it only by `kith mailbox add` there too.")
	return nil
}
