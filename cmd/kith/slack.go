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

	"github.com/EugeneShtoka/kith/internal/api"
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
	if !cfg.Slack.Enabled {
		return errors.New("set `enabled = true` under [slack] in the config, and restart kithd, first " +
			"(turning Slack on takes a restart; adding an account to it does not)")
	}
	account, err := setup.SlackAccount(cfg.Slack, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("kith login slack: %w", err)
	}
	token, cookie, err := readSlackSession(account.Name, setup.SlackWorkspace(account))
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
	if errors.Is(err, api.ErrNetworkOff) {
		return errors.New("kithd was started before [slack] was enabled; restart it " +
			"(`systemctl --user restart kithd`, or stop it and run kith) and sign in again")
	}
	if err != nil {
		return fmt.Errorf("sign in to Slack %s: %w", account.Name, err)
	}
	fmt.Printf("kith: signed in to %s as %s; its channels appear in kith now.\n", in.Workspace, in.User)
	return nil
}

// readSlackSession says where to copy a workspace's session from, and reads its token
// and cookie without echoing them.
func readSlackSession(name, workspace string) (token, cookie string, err error) {
	fmt.Printf(`Signing in to Slack %s (%s.slack.com).

kith signs in with the session your browser holds. In a browser signed in to
https://%[2]s.slack.com, open the developer tools (F12):

  1. Console — paste this line; it prints the token, which starts with xoxc-:
     Object.values(JSON.parse(localStorage.localConfig_v2).teams).find(t => t.url.includes("//%[2]s.")).token

  2. Application (Storage in Firefox) → Cookies → https://app.slack.com — copy the
     value of the cookie named d, which starts with xoxd-.

Both are a session: whoever has them can read and write as you. kith keeps them in
the system keyring. Signing out of Slack in that browser ends this session too.

`, name, workspace)
	token, err = readSecret("Token (xoxc-…)")
	if err != nil {
		return "", "", err
	}
	cookie, err = readSecret("Cookie d (xoxd-…)")
	if err != nil {
		return "", "", err
	}
	return token, cookie, checkSlackSession(token, cookie)
}

// checkSlackSession catches the two halves swapped or mistyped before Slack is asked.
func checkSlackSession(token, cookie string) error {
	if !strings.HasPrefix(token, "xoxc-") {
		return errors.New("the token starts with xoxc- — copy it from the console line above")
	}
	if !strings.HasPrefix(cookie, "xoxd-") {
		return errors.New("the cookie d starts with xoxd- — copy its value from the cookies of https://app.slack.com")
	}
	return nil
}
