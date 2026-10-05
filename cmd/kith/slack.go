package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/EugeneShtoka/kith/internal/setup"
)

// signInTimeout bounds asking Slack about a session and listing the workspace.
const signInTimeout = 2 * time.Minute

// runSlackLogin signs one [[slack.account]] in: kith uses the session a browser
// signed in to the workspace holds (its token and `d` cookie), which the person
// copies over; the daemon checks it with Slack, keeps it in the keyring and connects.
func runSlackLogin(args []string) error {
	fs := flag.NewFlagSet("login slack", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] the daemon runs as (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}
	if fs.NArg() > 1 {
		return errors.New("usage: kith login slack [account name]")
	}
	_, cfg, ready, err := loadConfig(*configPath, *profile)
	if err != nil || !ready {
		return err
	}
	if len(cfg.Slack.Accounts) == 0 {
		return errors.New("add a [[slack.account]] (name, workspace) to the config first, or run :login slack in kith")
	}
	account, err := setup.SlackAccount(cfg.Slack, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("kith login slack: %w", err)
	}
	token, cookie, err := readSlackSession(account.Name, account.Address())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	if at.note != "" {
		fmt.Fprintln(os.Stderr, "kith:", at.note)
	}
	// The daemon reads the config only when asked: an account just added to the file
	// is one it has not heard of yet.
	if err = at.backend.ReloadConfig(ctx); err != nil {
		return fmt.Errorf("have kithd re-read the config: %w", err)
	}
	in, err := at.backend.SignInSlack(ctx, account.Name, token, cookie)
	if err != nil {
		return fmt.Errorf("sign in to Slack %s: %w", account.Name, err)
	}
	fmt.Printf("kith: signed in to %s as %s; its channels appear in kith now.\n", in.Workspace, in.User)
	return nil
}

// readSlackSession says where to copy a workspace's session from, and reads its token
// and cookie without echoing them. workspace is its address, or its team ID.
func readSlackSession(name, workspace string) (token, cookie string, err error) {
	help := setup.SlackSession(workspace)
	fmt.Printf(`Signing in to Slack %s (%s).

kith signs in with the session your browser holds. In a browser signed in to it,
open %s, then the developer tools (F12):

  1. Console — paste this line; it prints the token, which starts with xoxc-:
     %s

  2. %s, which starts with xoxd-.

%s

`, name, help.Where, help.Open, help.Token, help.Cookie, setup.SlackSessionWarning)
	token, err = readSecret("Token (xoxc-…)")
	if err != nil {
		return "", "", err
	}
	cookie, err = readSecret("Cookie d (xoxd-…)")
	if err != nil {
		return "", "", err
	}
	return token, cookie, setup.CheckSlackSession(token, cookie)
}
