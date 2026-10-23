package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"
)

// ignoreTimeout bounds a single server round-trip (read-change-write of
// m.ignored_user_list on the homeserver).
const ignoreTimeout = 30 * time.Second

// runIgnore is `kith ignore <mxid>`: adds mxid to the Matrix account's
// m.ignored_user_list. The homeserver stops sending that user's events on every
// device of this account. Not a replacement for `[[tag]] name = "Spam"`: an
// ignored user leaves no history for a filter to catch, and the ignore does not
// reach any non-Matrix network.
func runIgnore(args []string) error {
	return runIgnoreOrUnignore(args, "ignore", func(ctx context.Context, backend ignoreBackend, mxid string) error {
		return backend.Ignore(ctx, mxid)
	})
}

// runUnignore is `kith unignore <mxid>`: drops mxid from the ignored list. A
// user who was not ignored is a no-op (no error, no server write).
func runUnignore(args []string) error {
	return runIgnoreOrUnignore(args, "unignore", func(ctx context.Context, backend ignoreBackend, mxid string) error {
		return backend.Unignore(ctx, mxid)
	})
}

// runIgnored is `kith ignored`: prints the MXIDs this account currently ignores,
// one per line, alphabetically. No daemon call fails this: no server, no list.
func runIgnored(args []string) error {
	fs := flag.NewFlagSet("ignored", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse ignored flags: %w", err)
	}
	if fs.NArg() != 0 {
		return errors.New("usage: kith ignored (lists the Matrix MXIDs this account ignores)")
	}
	ctx, cancel := reachIgnoreCtx()
	defer cancel()
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	ids, err := at.backend.Ignored(ctx)
	if err != nil {
		return fmt.Errorf("list the ignored users: %w", err)
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "No Matrix users are ignored.")
		return nil
	}
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

// ignoreBackend is the subset of the daemon's RPC surface the ignore subcommands
// call; it exists so tests can hand `runIgnoreOrUnignore` a fake.
type ignoreBackend interface {
	Ignore(ctx context.Context, mxid string) error
	Unignore(ctx context.Context, mxid string) error
}

func runIgnoreOrUnignore(args []string, verb string, do func(context.Context, ignoreBackend, string) error) error {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse %s flags: %w", verb, err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kith %s <mxid> (e.g. @alice:example.org)", verb)
	}
	mxid := strings.TrimSpace(fs.Arg(0))
	if !strings.HasPrefix(mxid, "@") || !strings.Contains(mxid, ":") {
		return fmt.Errorf("%q is not a Matrix user ID (expected @name:homeserver)", mxid)
	}
	ctx, cancel := reachIgnoreCtx()
	defer cancel()
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	if err := do(ctx, at.backend, mxid); err != nil {
		return fmt.Errorf("%s %s: %w", verb, mxid, err)
	}
	return nil
}

// reachIgnoreCtx is the lifetime of one subcommand: signals + ignoreTimeout.
func reachIgnoreCtx() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	ctx, cancel := context.WithTimeout(ctx, ignoreTimeout)
	return ctx, func() { cancel(); stop() }
}
